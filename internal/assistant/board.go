package assistant

// The board is the other way work gets done, beside the sessions the thread
// hands messages to: the thread puts a ticket on it, an orchestrator has it
// refined and then implemented, each by a session of its own that sees only
// the ticket, and tells the thread what came of it. The board plugin has the
// tools; the thread is told here when to use them, as it is of its own.

// The tools of the board for the thread.
const (
	ToolTicketCreate  = "ticket_create"
	ToolTicketComment = "ticket_comment"
	ToolBoardRead     = "board_read"
)

// threadBoardHint tells the agent of the thread of the board, where it has
// its tools: when work is a ticket and when a session, and what it does with
// what the board tells it.
const threadBoardHint = `Besides the sessions there is the board, for work that is more than a quick answer. ` +
	`A ticket you put on it with ` + ToolTicketCreate + ` is first refined, by a session that works out what exactly is to be done and asks what only the owner can answer, and then implemented by another: both see the ticket alone, never this conversation, and never appear in your list of sessions. ` +
	`Decide for every new concern which way it goes. It is a ticket when it is real work: building or changing something in a project, a task of several steps, anything that should be thought through before it is done or whose result the owner will want to review. ` +
	`It stays a session, with ` + ToolSessionNew + `, when it is small, like a lookup, a quick research or a question about something, or when the owner says that it has to be fast: the board takes minutes before work on a ticket begins. When in doubt for real work, make it a ticket. ` +
	`Write the ticket so that it stands for itself: a short title, and in its text what the owner wants in their words and language, with what of this conversation is needed to understand it, and the project directory where you know it. Do not work out a solution: the refinement does. ` +
	`After ` + ToolTicketCreate + ` say one short acknowledgement and end your answer, as after a hand-off. ` +
	`What the board has to tell you arrives as an internal background result. A ticket that is done: tell the owner what was done, in your words. A ticket that is blocked and asks the owner something: ask them, as you ask for a session, and write what they answer to the ticket with ` + ToolTicketComment + `, in their words; the board goes on from there by itself. Never hand such an answer to a session, and never answer a question of a ticket yourself unless the owner already said it here. ` +
	`What the owner adds to a ticket later, corrects in it, or that they want it dropped, you also write to it with ` + ToolTicketComment + `: a session that works on it is told at once. ` +
	`You are not told the tickets as you are the sessions: when the owner asks how things stand, or says something that may belong to a ticket you do not see here, look with ` + ToolBoardRead + ` before you decide where it goes. ` +
	`You may speak of tickets to the owner, by their number and title: they see the board too. `
