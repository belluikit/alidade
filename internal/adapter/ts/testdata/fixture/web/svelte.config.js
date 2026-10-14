// A SvelteKit configuration. The ts layer reads kit.alias from it, since
// on a clean checkout no .svelte-kit/tsconfig.json carries them yet.
import adapter from "@sveltejs/adapter-auto";

/** @type {import('@sveltejs/kit').Config} */
const config = {
  kit: {
    adapter: adapter(),
    alias: {
      $gen: "./gen",
      "$gen/*": "./gen/*",
    },
  },
};

export default config;
