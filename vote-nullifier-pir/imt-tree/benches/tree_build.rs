use criterion::{criterion_group, criterion_main, Criterion};
use ff::Field;
use pasta_curves::Fp;

fn bench_tree_build(c: &mut Criterion) {
    let mut rng = rand::thread_rng();

    // 100k nullifiers — big enough to exercise parallel hashing,
    // small enough to finish in reasonable benchmark time.
    let nfs: Vec<Fp> = (0..100_000).map(|_| Fp::random(&mut rng)).collect();

    c.bench_function("build_sentinel_tree_100k", |b| {
        b.iter(|| {
            imt_tree::tree::build_sentinel_tree(&nfs).unwrap();
        });
    });
}

criterion_group! {
    name = benches;
    config = Criterion::default().sample_size(10);
    targets = bench_tree_build
}
criterion_main!(benches);
