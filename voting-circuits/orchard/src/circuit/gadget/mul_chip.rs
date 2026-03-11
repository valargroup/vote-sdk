use halo2_proofs::{
    circuit::{AssignedCell, Chip, Layouter},
    plonk::{self, Advice, Column, ConstraintSystem, Constraints, Selector},
    poly::Rotation,
};
use pasta_curves::pallas;

use super::MulInstruction;

/// Configuration for the multiplication chip.
#[derive(Clone, Debug)]
pub struct MulConfig {
    a: Column<Advice>,
    b: Column<Advice>,
    c: Column<Advice>,
    q_mul: Selector,
}

/// A chip implementing a single multiplication constraint `c = a * b` on a single row.
#[derive(Debug)]
pub struct MulChip {
    config: MulConfig,
}

impl Chip<pallas::Base> for MulChip {
    type Config = MulConfig;
    type Loaded = ();

    fn config(&self) -> &Self::Config {
        &self.config
    }

    fn loaded(&self) -> &Self::Loaded {
        &()
    }
}

impl MulChip {
    /// Configures the multiplication chip with the given advice columns.
    pub fn configure(
        meta: &mut ConstraintSystem<pallas::Base>,
        a: Column<Advice>,
        b: Column<Advice>,
        c: Column<Advice>,
    ) -> MulConfig {
        let q_mul = meta.selector();
        meta.create_gate("Field element multiplication: c = a * b", |meta| {
            let q_mul = meta.query_selector(q_mul);
            let a = meta.query_advice(a, Rotation::cur());
            let b = meta.query_advice(b, Rotation::cur());
            let c = meta.query_advice(c, Rotation::cur());

            Constraints::with_selector(q_mul, Some(a * b - c))
        });

        MulConfig { a, b, c, q_mul }
    }

    /// Constructs a multiplication chip from the given config.
    pub fn construct(config: MulConfig) -> Self {
        Self { config }
    }
}

impl MulInstruction<pallas::Base> for MulChip {
    fn mul(
        &self,
        mut layouter: impl Layouter<pallas::Base>,
        a: &AssignedCell<pallas::Base, pallas::Base>,
        b: &AssignedCell<pallas::Base, pallas::Base>,
    ) -> Result<AssignedCell<pallas::Base, pallas::Base>, plonk::Error> {
        layouter.assign_region(
            || "c = a * b",
            |mut region| {
                self.config.q_mul.enable(&mut region, 0)?;

                a.copy_advice(|| "copy a", &mut region, self.config.a, 0)?;
                b.copy_advice(|| "copy b", &mut region, self.config.b, 0)?;

                let scalar_val = a.value().zip(b.value()).map(|(a, b)| *a * *b);
                region.assign_advice(|| "c", self.config.c, 0, || scalar_val)
            },
        )
    }
}
