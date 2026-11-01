package crew

// Bounds is what this package holds a crew, a task's result and a
// member's messages to, read from the constants that enforce them, so
// the operating guide (internal/api, guide.go) can never say other than
// what Validate, Submit and SendMessage refuse.
type Bounds struct {
	// TaskIDPattern is a task's and a check's id (ValidTaskID).
	TaskIDPattern string
	// Members and Tasks a crew; ChecksPerTask a task.
	Members, Tasks, ChecksPerTask int
	// CheckTimeoutMin and CheckTimeoutMax bound a check's timeoutSeconds,
	// whose 0 is CheckTimeoutDefault.
	CheckTimeoutMin, CheckTimeoutMax, CheckTimeoutDefault int
	// Summary is a result's summary, in characters; Issues and Findings
	// its lists.
	Summary, Issues, Findings int
	// MessageChars is a member's message, in characters; Inbox the
	// messages a member keeps not acknowledged.
	MessageChars, Inbox int
}

// TheBounds is the package's bounds as it enforces them.
func TheBounds() Bounds {
	return Bounds{
		TaskIDPattern:       taskIDPattern.String(),
		Members:             maxMembers,
		Tasks:               maxTasks,
		ChecksPerTask:       maxChecks,
		CheckTimeoutMin:     1,
		CheckTimeoutMax:     maxCheckTimeout,
		CheckTimeoutDefault: defaultCheckTimeout,
		Summary:             maxSummary,
		Issues:              maxIssues,
		Findings:            maxFindings,
		MessageChars:        maxMessageChars,
		Inbox:               maxInbox,
	}
}
