import { create } from "@bufbuild/protobuf";
import { UserSchema, Role, type User } from "$gen/app/v1/user_pb.js";
import { helper } from "../util";

export function newUser(email: string): User {
  helper();
  return create(UserSchema, { id: "u1", email, role: Role.VIEWER });
}
