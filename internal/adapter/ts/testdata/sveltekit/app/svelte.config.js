// A SvelteKit app never synced: its .svelte-kit/tsconfig.json, which the
// app's tsconfig extends, is missing, as in a fresh clone (#232).
const config = {
  kit: {
    alias: {
      $gen: "../gen",
    },
  },
};

export default config;
