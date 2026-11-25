// A build script that fails, as one does when protoc is missing: what it
// would have written is never there.
fn main() {
    println!("need PROTOBUF");
    eprintln!("protoc not found");
    panic!("no protoc");
}
