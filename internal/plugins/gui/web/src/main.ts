import { mount } from 'svelte';
import App from './App.svelte';
import { Chat } from './lib/chat.svelte';
import { connect } from './lib/bridge';
import { locale } from './lib/i18n';
import './app.css';

document.documentElement.lang = locale();

const target = document.getElementById('app');
if (target) {
  const link = connect();
  mount(App, { target, props: { chat: link ? new Chat(link) : null } });
}
