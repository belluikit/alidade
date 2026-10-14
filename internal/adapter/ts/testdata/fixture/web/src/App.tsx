import React from "react";
import { newUser } from "./lib/api";

export function App() {
  const u = newUser("x@y.z");
  return <main>{u.email}</main>;
}
