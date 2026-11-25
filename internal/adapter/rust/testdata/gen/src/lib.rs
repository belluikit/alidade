// What tonic's include_proto! does: an include! the index shows only in
// the macro's definition, not where it is called.
macro_rules! include_proto {
    ($package: tt) => {
        include!(concat!(env!("OUT_DIR"), concat!("/", $package, ".rs")));
    };
}

pub mod pb {
    include!(concat!(
        env!("OUT_DIR"),
        "/fixture.shapes.rs"
    ));
}

include!(concat!(env!("OUT_DIR"), "/_.rs"));

pub fn unit_name() -> &'static str {
    pb::shape::Unit::Mm.as_str_name()
}

pub mod wrapped {
    include_proto!("wrapped");
}

pub fn via() -> wrapped::Via {
    wrapped::Via {}
}
