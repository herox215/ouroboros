import { mount } from 'svelte';
import App from './App.svelte';
import { Chat } from './lib/chat.svelte';
import { Board } from './lib/board.svelte';
import { Sessions } from './lib/sessions.svelte';
import { Agents } from './lib/agents.svelte';
import { Jobs } from './lib/jobs.svelte';
import { Themes } from './lib/themes.svelte';
import { NodeOptions } from './lib/nodeoptions.svelte';
import { connect } from './lib/bridge';
import { locale } from './lib/i18n';
import './app.css';

document.documentElement.lang = locale();
// The theme is worn before the first paint, so that the window does not show
// the olive of app.css for a moment first.
const themes = new Themes();

const target = document.getElementById('app');
if (target) {
  const host = connect();
  const agents = host ? new Agents(host.agents) : null;
  const node = host?.options ? new NodeOptions(host.options) : null;
  // An access that was added, signed in to anew or removed offers other
  // models: the agents list them anew.
  if (node && agents) node.onaccesses = () => (agents.models = null);
  mount(App, {
    target,
    props: {
      chat: host ? new Chat(host) : null,
      board: host ? new Board(host.board) : null,
      sessions: host ? new Sessions(host.sessions) : null,
      agents,
      jobs: host ? new Jobs(host.agents) : null,
      themes,
      node,
    },
  });
}
