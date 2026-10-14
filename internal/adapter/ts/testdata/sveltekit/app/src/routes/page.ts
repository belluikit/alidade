import { helper } from '$lib/util';
import { generated } from '$gen/api';

export function load() {
  return helper() + generated();
}
