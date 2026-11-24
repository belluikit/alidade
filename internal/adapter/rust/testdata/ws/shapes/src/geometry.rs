use crate::{Area, Square};

pub struct Point {
    pub x: f64,
    pub y: f64,
    /// A raw identifier: the field is named type.
    pub r#type: u8,
}

pub fn unit() -> Square {
    Square::new(1.0)
}

pub fn unit_area() -> f64 {
    unit().area()
}
