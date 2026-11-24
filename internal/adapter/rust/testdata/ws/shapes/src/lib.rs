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
        fn deepest() -> f64 {
            1.0
        }
        area_of(&Square::new(2.0)) + deepest()
    }
    inner()
}

/// A macro that writes two functions: rust-analyzer gives both the range of
/// the whole call, and they are siblings, not one inside the other.
macro_rules! two_fns {
    ($a:ident, $b:ident) => {
        pub fn $a() -> u8 {
            1
        }
        pub fn $b() -> u8 {
            2
        }
    };
}

two_fns!(made_first, made_second);

pub fn local_impl() -> f64 {
    struct Local;
    impl Area for Local {
        fn area(&self) -> f64 {
            1.0
        }
    }
    Local.area()
}

/// fmt::Write implemented inside a body: joined by name, it would be
/// geometry::Write, which has no write_str.
pub fn pen() -> String {
    struct Pen(String);
    impl fmt::Write for Pen {
        fn write_str(&mut self, s: &str) -> fmt::Result {
            self.0.push_str(s);
            Ok(())
        }
    }
    let mut p = Pen(String::new());
    let _ = fmt::Write::write_str(&mut p, "x");
    p.0
}

/// A comment ahead of the header on its line puts a column the server and
/// the index count differently, in UTF-16 and UTF-8, before the type.
pub struct Wide;

/* größe */ impl Area for Wide { fn area(&self) -> f64 { 0.0 } }

/// A blanket implementation: its type is a parameter, with no node.
pub trait Named {
    fn name(&self) -> String;
}

impl<T: Area> Named for T {
    fn name(&self) -> String {
        String::new()
    }
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
