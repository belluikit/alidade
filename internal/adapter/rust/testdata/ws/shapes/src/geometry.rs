use crate::{Area, Square};

pub struct Point {
    pub x: f64,
    pub y: f64,
    /// A raw identifier: the field is named type.
    pub r#type: u8,
}

/// A workspace trait that shares a name with fmt::Write, and none of its
/// methods.
pub trait Write {
    fn line(&mut self, s: &str);
}

/// A workspace trait that shares a name and a method with fmt::Display,
/// which Square implements: the server tells them apart, a name would not.
pub trait Display {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result;
}

pub fn unit() -> Square {
    Square::new(1.0)
}

pub fn unit_area() -> f64 {
    unit().area()
}
