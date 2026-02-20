use criterion::{criterion_group, criterion_main, Criterion};
use halo2_proofs::{
    plonk::{self, SingleVerifier},
    transcript::{Blake2bRead, Blake2bWrite},
};
use pasta_curves::{pallas, vesta};
use rand::{rngs::OsRng, RngCore};

use ff::{Field, PrimeField};
use incrementalmerkletree::{Hashable, Level};
use std::{
    alloc::{GlobalAlloc, Layout, System},
    sync::atomic::{AtomicUsize, Ordering},
};

use orchard::{
    delegation::{
        builder::{build_delegation_bundle, DelegationBundle, RealNoteInput},
        imt::{ImtProvider, SpacedLeafImtProvider},
    },
    keys::{FullViewingKey, Scope, SpendingKey},
    note::{ExtractedNoteCommitment, Note, RandomSeed, Rho},
    tree::{MerkleHashOrchard, MerklePath},
    value::NoteValue,
};

const K: u32 = 14;
const MERKLE_DEPTH: usize = 32;

struct TrackingAllocator;

static LIVE_ALLOCATED_BYTES: AtomicUsize = AtomicUsize::new(0);

#[global_allocator]
static GLOBAL: TrackingAllocator = TrackingAllocator;

unsafe impl GlobalAlloc for TrackingAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let ptr = unsafe { System.alloc(layout) };
        if !ptr.is_null() {
            LIVE_ALLOCATED_BYTES.fetch_add(layout.size(), Ordering::Relaxed);
        }
        ptr
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        LIVE_ALLOCATED_BYTES.fetch_sub(layout.size(), Ordering::Relaxed);
        unsafe { System.dealloc(ptr, layout) };
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        let new_ptr = unsafe { System.realloc(ptr, layout, new_size) };
        if !new_ptr.is_null() {
            if new_size >= layout.size() {
                LIVE_ALLOCATED_BYTES.fetch_add(new_size - layout.size(), Ordering::Relaxed);
            } else {
                LIVE_ALLOCATED_BYTES.fetch_sub(layout.size() - new_size, Ordering::Relaxed);
            }
        }
        new_ptr
    }
}

fn live_allocated_bytes() -> usize {
    LIVE_ALLOCATED_BYTES.load(Ordering::Relaxed)
}

fn measured_heap_usage_via_clone<T: Clone>(value: &T) -> usize {
    let cloned = value.clone();
    let after_clone = live_allocated_bytes();
    drop(cloned);
    let after_drop = live_allocated_bytes();
    after_clone.saturating_sub(after_drop)
}

fn format_bytes(bytes: usize) -> String {
    format!("{bytes} bytes ({:.2} KiB)", bytes as f64 / 1024.0)
}

/// Create a note using only public APIs, returning (note, nullifier_field_element).
fn make_note(recipient: orchard::Address, value: NoteValue, rng: &mut impl RngCore) -> Note {
    // Generate a random nullifier for rho.
    loop {
        let mut rho_bytes = [0u8; 32];
        rng.fill_bytes(&mut rho_bytes);
        let rho = Rho::from_bytes(&rho_bytes);
        if bool::from(rho.is_none()) {
            continue;
        }
        let rho = rho.unwrap();
        let mut rseed_bytes = [0u8; 32];
        rng.fill_bytes(&mut rseed_bytes);
        let rseed = RandomSeed::from_bytes(rseed_bytes, &rho);
        if bool::from(rseed.is_none()) {
            continue;
        }
        let note = Note::from_parts(recipient, value, rho, rseed.unwrap());
        if bool::from(note.is_some()) {
            return note.unwrap();
        }
    }
}

/// Get the nullifier of a note as a pallas::Base field element.
fn nullifier_base(note: &Note, fvk: &FullViewingKey) -> pallas::Base {
    pallas::Base::from_repr(note.nullifier(fvk).to_bytes()).unwrap()
}

/// Helper: create 1-4 real note inputs with a shared Merkle tree and anchor.
fn make_real_note_inputs(
    fvk: &FullViewingKey,
    values: &[u64],
    imt_provider: &impl ImtProvider,
    rng: &mut impl RngCore,
) -> (Vec<RealNoteInput>, pallas::Base) {
    let n = values.len();
    assert!(n >= 1 && n <= 4);

    let recipient = fvk.address_at(0u32, Scope::External);
    let mut notes = Vec::with_capacity(n);
    for &v in values {
        notes.push(make_note(recipient, NoteValue::from_raw(v), rng));
    }

    let empty_leaf = MerkleHashOrchard::empty_leaf();
    let mut leaves = [empty_leaf; 4];
    for (i, note) in notes.iter().enumerate() {
        let cmx = ExtractedNoteCommitment::from(note.commitment());
        leaves[i] = MerkleHashOrchard::from_cmx(&cmx);
    }

    let l1_0 = MerkleHashOrchard::combine(Level::from(0), &leaves[0], &leaves[1]);
    let l1_1 = MerkleHashOrchard::combine(Level::from(0), &leaves[2], &leaves[3]);
    let l2_0 = MerkleHashOrchard::combine(Level::from(1), &l1_0, &l1_1);

    let mut current = l2_0;
    for level in 2..MERKLE_DEPTH {
        let sibling = MerkleHashOrchard::empty_root(Level::from(level as u8));
        current = MerkleHashOrchard::combine(Level::from(level as u8), &current, &sibling);
    }
    let nc_root = pallas::Base::from_repr(current.to_bytes())
        .expect("MerkleHashOrchard always contains a valid field element");

    let l1 = [l1_0, l1_1];
    let mut inputs = Vec::with_capacity(n);
    for (i, note) in notes.into_iter().enumerate() {
        let mut auth_path = [MerkleHashOrchard::empty_leaf(); MERKLE_DEPTH];
        auth_path[0] = leaves[i ^ 1];
        auth_path[1] = l1[1 - (i >> 1)];
        for level in 2..MERKLE_DEPTH {
            auth_path[level] = MerkleHashOrchard::empty_root(Level::from(level as u8));
        }
        let merkle_path = MerklePath::from_parts(i as u32, auth_path);

        let nf = nullifier_base(&note, fvk);
        let imt_proof = imt_provider
            .non_membership_proof(nf)
            .expect("non_membership_proof");

        inputs.push(RealNoteInput {
            note,
            fvk: fvk.clone(),
            merkle_path,
            imt_proof,
            scope: Scope::External,
        });
    }

    (inputs, nc_root)
}

/// Build a delegation bundle with the given note values.
fn build_test_bundle(values: &[u64]) -> DelegationBundle {
    let mut rng = OsRng;
    let sk = SpendingKey::from_bytes([7; 32]).unwrap();
    let fvk: FullViewingKey = (&sk).into();
    let output_recipient = fvk.address_at(1u32, Scope::External);
    let vote_round_id = pallas::Base::random(&mut rng);
    let van_comm_rand = pallas::Base::random(&mut rng);
    let alpha = pallas::Scalar::random(&mut rng);

    let imt = SpacedLeafImtProvider::new();
    let (inputs, nc_root) = make_real_note_inputs(&fvk, values, &imt, &mut rng);

    build_delegation_bundle(
        inputs,
        &fvk,
        alpha,
        output_recipient,
        vote_round_id,
        nc_root,
        van_comm_rand,
        &imt,
        &mut rng,
    )
    .unwrap()
}

fn criterion_benchmark(c: &mut Criterion) {
    // Build a valid bundle (1 real note + 3 padded).
    let bundle = build_test_bundle(&[13_000_000]);
    let pi = bundle.instance.to_halo2_instance();
    let instance_column = pi.clone();
    let instance_columns = [&instance_column[..]];
    let instances = [&instance_columns[..]];

    // Generate params and keys.
    let params = halo2_proofs::poly::commitment::Params::<vesta::Affine>::new(K);
    let keygen_circuit = bundle.circuit.clone();
    let vk = plonk::keygen_vk(&params, &keygen_circuit).unwrap();
    let pk = plonk::keygen_pk(&params, vk.clone(), &keygen_circuit).unwrap();

    // This halo2 version does not expose key serialization APIs, so we report in-memory
    // size as: stack footprint + retained heap bytes observed for a cloned key.
    let vk_stack_bytes = std::mem::size_of_val(&vk);
    let pk_stack_bytes = std::mem::size_of_val(&pk);
    let vk_heap_bytes = measured_heap_usage_via_clone(&vk);
    let pk_heap_bytes = measured_heap_usage_via_clone(&pk);
    let vk_size_bytes = vk_stack_bytes + vk_heap_bytes;
    let pk_size_bytes = pk_stack_bytes + pk_heap_bytes;
    eprintln!(
        "delegation key sizes (in-memory) -> vk: {} [stack {}, heap {}], pk: {} [stack {}, heap {}]",
        format_bytes(vk_size_bytes),
        format_bytes(vk_stack_bytes),
        format_bytes(vk_heap_bytes),
        format_bytes(pk_size_bytes),
        format_bytes(pk_stack_bytes),
        format_bytes(pk_heap_bytes),
    );

    // Sanity-check with MockProver.
    let mock = halo2_proofs::dev::MockProver::run(K, &bundle.circuit, vec![pi.clone()]).unwrap();
    mock.verify().expect("MockProver failed");

    // Generate one proof up-front for verify benchmarks.
    let proof_bytes = {
        let mut transcript = Blake2bWrite::<_, vesta::Affine, _>::init(vec![]);
        plonk::create_proof(
            &params,
            &pk,
            &[bundle.circuit.clone()],
            &instances,
            &mut OsRng,
            &mut transcript,
        )
        .unwrap();
        transcript.finalize()
    };

    {
        let mut group = c.benchmark_group("delegation-keygen");
        group.sample_size(10);
        let keygen_circuit = bundle.circuit.clone();
        group.bench_function("keygen", |b| {
            b.iter(|| {
                let vk = plonk::keygen_vk(&params, &keygen_circuit).unwrap();
                let _pk = plonk::keygen_pk(&params, vk, &keygen_circuit).unwrap();
            });
        });
    }

    {
        let mut group = c.benchmark_group("delegation-proving");
        group.sample_size(10);
        let circuit = bundle.circuit.clone();
        group.bench_function("prove", |b| {
            b.iter(|| {
                let mut transcript = Blake2bWrite::<_, vesta::Affine, _>::init(vec![]);
                plonk::create_proof(
                    &params,
                    &pk,
                    &[circuit.clone()],
                    &instances,
                    &mut OsRng,
                    &mut transcript,
                )
                .unwrap();
                transcript.finalize()
            });
        });
    }

    {
        let mut group = c.benchmark_group("delegation-verifying");
        group.bench_function("verify", |b| {
            b.iter(|| {
                let strategy = SingleVerifier::new(&params);
                let mut transcript = Blake2bRead::init(&proof_bytes[..]);
                plonk::verify_proof(&params, &vk, strategy, &instances, &mut transcript).unwrap();
            });
        });
    }
}

criterion_group! {
    name = benches;
    config = Criterion::default();
    targets = criterion_benchmark
}
criterion_main!(benches);
