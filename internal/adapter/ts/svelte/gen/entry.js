// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// The script the V8 converter runs: svelte2tsx, as svelte-check calls it,
// behind one global function the Go side calls per component.
import { svelte2tsx } from 'svelte2tsx';
globalThis.convert = (src, filename) => {
  const r = svelte2tsx(src, { filename, isTsFile: true, mode: 'ts' });
  return JSON.stringify({ code: r.code, mappings: r.map.mappings });
};
