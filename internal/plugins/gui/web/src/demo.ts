import { mount } from 'svelte';
import App from './App.svelte';
import { Chat } from './lib/chat.svelte';
import { demoLink } from './lib/demo';
import { locale, setLocale } from './lib/i18n';
import './app.css';

// A scripted thread for looking at the window in a browser while it is built
// (pnpm dev, then /demo.html). It is not part of the build the binary embeds.
setLocale('de');
document.documentElement.lang = locale();

const target = document.getElementById('app');
if (target) {
  mount(App, { target, props: { chat: new Chat(demoLink()) } });
}
