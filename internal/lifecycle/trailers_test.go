package lifecycle

import "testing"

func TestStripAgentTrailers_RemovesAIAttributionOnly(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "co-author naming an AI",
			in:   "Do the thing\n\nBody stays.\n\nCo-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>\n",
			want: "Do the thing\n\nBody stays.\n",
		},
		{
			name: "session URL trailer",
			in:   "Do the thing\n\nClaude-Session: https://claude.ai/code/session_01G5\n",
			want: "Do the thing\n",
		},
		{
			name: "both, plus a human co-author that stays",
			in: "Do the thing\n\nCo-Authored-By: Alice <alice@example.com>\n" +
				"Co-Authored-By: Claude <noreply@anthropic.com>\n" +
				"Claude-Session: https://claude.ai/x\n",
			want: "Do the thing\n\nCo-Authored-By: Alice <alice@example.com>\n",
		},
		{
			name: "other vendors",
			in:   "Do it\n\nCo-Authored-By: Copilot <copilot@github.com>\nCo-Authored-By: ChatGPT <noreply@openai.com>\n",
			want: "Do it\n",
		},
		{
			name: "nothing to strip is returned unchanged",
			in:   "Do the thing\n\nA normal body.\n\nSigned-off-by: Someone <s@example.com>\n",
			want: "Do the thing\n\nA normal body.\n\nSigned-off-by: Someone <s@example.com>\n",
		},
		{
			name: "prose mentioning claude is not a trailer",
			in:   "Document the drift\n\nClaude Code was described as installed by default.\n",
			want: "Document the drift\n\nClaude Code was described as installed by default.\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripAgentTrailers(tc.in); got != tc.want {
				t.Errorf("stripAgentTrailers()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
