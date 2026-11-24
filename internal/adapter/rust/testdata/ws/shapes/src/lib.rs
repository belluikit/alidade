//! Shapes: the fixture of the Rust layer (ADR 009).

use std::fmt;

pub mod geometry;

/// Code a build script writes (ADR 009): `Made` lives in Cargo's output directory.
pub mod made {
    include!(concat!(env!("OUT_DIR"), "/made.rs"));
}

pub const SIDES: u32 = 4;
pub static ORIGIN: (i32, i32) = (0, 0);

pub trait Area {
    fn area(&self) -> f64;
}

#[derive(Default)]
pub struct Square {
    pub side: f64,
}

pub enum Kind {
    Round,
    Angular,
}

impl Square {
    pub fn new(side: f64) -> Square {
        Square { side }
    }
}

impl Area for Square {
    fn area(&self) -> f64 {
        self.side * self.side
    }
}

impl fmt::Display for Square {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "square {}", self.side)
    }
}

impl fmt::Debug for Square {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "Square({})", self.side)
    }
}

macro_rules! round_shape {
    ($t:ident, $r:ident) => {
        pub struct $t {
            pub $r: f64,
        }

        impl Area for $t {
            fn area(&self) -> f64 {
                3.0 * self.$r * self.$r
            }
        }
    };
}

round_shape!(Circle, radius);

pub fn area_of(s: &Square) -> f64 {
    s.area()
}

pub fn total(shapes: &[Square]) -> f64 {
    shapes.iter().map(area_of).sum()
}

pub fn parse_side(s: &str) -> u8 {
    s.parse::<u8>().unwrap_or(0) + helper_generic::<u8>(1)
}

fn helper_generic<T: Into<u8>>(v: T) -> u8 {
    v.into()
}

pub fn outer() -> f64 {
    fn inner() -> f64 {
        area_of(&Square::new(2.0))
    }
    inner()
}

pub fn local_impl() -> f64 {
    struct Local;
    impl Area for Local {
        fn area(&self) -> f64 {
            1.0
        }
    }
    Local.area()
}

pub fn made() -> made::Made {
    made::Made::new()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn check(s: &Square) -> bool {
        area_of(s) > 0.0
    }

    #[test]
    fn squares() {
        assert!(check(&Square::new(1.0)));
    }
}
