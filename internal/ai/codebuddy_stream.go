package ai

// buildCodebuddyStreamArgs constructs the CLI arguments for Codebuddy streaming
func buildCodebuddyStreamArgs(req ChatRequest) []string {
	return buildBaseStreamArgs(req, func(r ChatRequest) []string {
		return []string{"--disallowedTools", "CronCreate,CronDelete,CronList,ToolSearch,DeferExecuteTool,AskUserQuestion"}
	})
}
