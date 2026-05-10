package skill

import (
	"errors"
	"fmt"
)

var (
	ErrSkillNotFound       = errors.New("skill not found")
	ErrSkillInvalidRequest = errors.New("invalid skill request")
	ErrSkillConflict       = errors.New("skill conflict")
)

func newSkillNotFoundError(name string) error {
	return fmt.Errorf("%w: %s", ErrSkillNotFound, name)
}

func newSkillInvalidRequestError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSkillInvalidRequest, fmt.Sprintf(format, args...))
}

func newSkillConflictError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSkillConflict, fmt.Sprintf(format, args...))
}
