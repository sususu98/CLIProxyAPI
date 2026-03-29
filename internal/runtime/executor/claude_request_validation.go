package executor

import (
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

func rejectInvalidClaudeMessagesRequest(payload []byte) error {
	return toClaudeStatusErr(util.ValidateClaudeMessagesRequest(payload))
}

func rejectUnsupportedClaudeImages(payload []byte) error {
	return toClaudeStatusErr(util.ValidateClaudeImagesForGoogleUpstream(payload))
}

func toClaudeStatusErr(validationErr *util.ClaudeRequestValidationError) error {
	if validationErr == nil {
		return nil
	}
	return statusErr{
		code: http.StatusBadRequest,
		msg:  fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","message":%q}}`, validationErr.Message),
	}
}
