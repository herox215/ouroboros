import { mount } from 'svelte';
import App from './App.svelte';
import { Chat } from './lib/chat.svelte';
import { Board } from './lib/board.svelte';
import { Sessions } from './lib/sessions.svelte';
import { Agents } from './lib/agents.svelte';
import { Jobs } from './lib/jobs.svelte';
import { connect } from './lib/bridge';
import { locale } from './lib/i18n';
import './app.css';

document.documentElement.lang = locale();

const target = document.getElementById('app');
if (target) {
  const host = connect();
  mount(App, {
    target,
    props: {
      chat: host ? new Chat(host) : null,
      board: host ? new Board(host.board) : null,
      sessions: host ? new Sessions(host.sessions) : null,
      agents: host ? new Agents(host.agents) : null,
      jobs: host ? new Jobs(host.agents) : null,
    },
  });
}
