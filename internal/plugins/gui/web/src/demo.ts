import { mount } from 'svelte';
import App from './App.svelte';
import { Chat } from './lib/chat.svelte';
import { Board } from './lib/board.svelte';
import { Sessions } from './lib/sessions.svelte';
import { Agents } from './lib/agents.svelte';
import { Jobs } from './lib/jobs.svelte';
import { Memories } from './lib/memories.svelte';
import { Themes } from './lib/themes.svelte';
import { demoAgents, demoBoard, demoLink, demoMemories, demoOptions, demoSessions } from './lib/demo';
import { NodeOptions } from './lib/nodeoptions.svelte';
import { locale, setLocale } from './lib/i18n';
import './app.css';

// A scripted thread for looking at the window in a browser while it is built
// (pnpm dev, then /demo.html). It is not part of the build the binary embeds.
setLocale('de');
document.documentElement.lang = locale();

const target = document.getElementById('app');
if (target) {
  const agents = demoAgents();
  mount(App, {
    target,
    props: {
      chat: new Chat(demoLink()),
      board: new Board(demoBoard()),
      sessions: new Sessions(demoSessions()),
      agents: new Agents(agents),
      jobs: new Jobs(agents),
      memories: new Memories(demoMemories()),
      themes: new Themes(),
      node: new NodeOptions(demoOptions()),
    },
  });
}
