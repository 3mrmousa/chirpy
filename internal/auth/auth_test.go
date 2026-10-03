package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestJWT(t *testing.T) {
	mySecret := "123"
	userUUID := uuid.Must(uuid.Parse("4e908fa7-a07a-4b97-ab89-fcd70720384f"))

	token, err := MakeJWT(userUUID, mySecret, time.Hour)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	parsedID, err := ValidateJWT(token, mySecret)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if parsedID != userUUID {
		t.Fatalf("Expected %v, got %v", userUUID, parsedID)
	}
}

