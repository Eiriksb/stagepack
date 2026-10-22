// Package ui is what the person presenting sees of a running deck: a tray icon
// with a small menu on Windows, or JSON lines on stdout when headless (tests,
// other OSes).
package ui

import "stagepack/internal/save"

// Actions are what the person can ask for. The UI calls them from its own
// goroutines; each may block (Stop waits for the last save).
type Actions struct {
	OpenPresenter, OpenAudience, SaveCopy, Stop func()
}

// UI shows a running deck.
type UI interface {
	// Run blocks until Quit, and must be called on the main goroutine. It calls
	// ready (once) when the UI is up; ready must not block.
	Run(ready func())
	// SetStatus shows whether the notes are in the file. It must not block:
	// it is called while the save watcher holds its lock.
	SetStatus(title string, s save.Status, detail string)
	// Opening shows that the talk called name is being opened, which can take a
	// while, until done is called. It must not block. Called before Run.
	Opening(name string) (done func())
	// Notify shows a message. It must not block once Run has been called;
	// before that it may wait until the message has been seen, as the program
	// may end right after.
	Notify(title, msg string)
	Error(title, msg string)
	// Confirm asks a yes/no question and waits for the answer; true is yes. It
	// is called before Run, and before anything has been started.
	Confirm(title, msg string) bool
	// AskSavePath asks where to save a copy of the deck, suggesting suggest.
	AskSavePath(suggest string) (string, bool)
	// Quit makes Run return. It is safe to call more than once, and before Run.
	Quit()
}
