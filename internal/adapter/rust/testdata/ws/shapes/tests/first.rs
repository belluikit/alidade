mod common;

fn helper() -> f64 {
    shapes::area_of(&common::sample())
}

#[test]
fn first() {
    assert!(helper() > 0.0);
}
