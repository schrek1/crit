{{if eq .comment_count 1}}The reviewer sent you 1 comment during the review.{{else}}The reviewer sent you {{.comment_count}} comments during the review.{{end}} This is not Finish Review — do not stop the review loop.

{{.comments_json}}

Address each comment. Run `crit comment --status seen <id>` when you have read it and `crit comment --status working <id>` before you start on it. Reply with `crit comment --reply-to <comment-id> --author <your-name> "<explanation>"`. Put your explanations in these replies, not in chat.

When you're done, run:

  {{.next_round_cmd}}
