import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

// Runes everywhere: the chat is a few stores with $state, not legacy props and
// stores, so that what each component reads is told by the code itself.
export default {
  preprocess: vitePreprocess(),
  compilerOptions: {
    runes: true,
  },
};
