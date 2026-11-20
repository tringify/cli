package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tringify/cli/internal/devapi"
)

// identityCode asks for the identity check a protected action needs: a code
// from the authenticator app, or, when the developer presses Enter, a code
// Tringify emails for exactly this action and target.
func (a *app) identityCode(ctx context.Context, api *devapi.Client, action, targetType, targetID, given string) (string, error) {
	if code := strings.TrimSpace(given); code != "" {
		return code, nil
	}
	reader := bufio.NewReader(a.stdin)
	a.printf("Enter a code from your authenticator app, or press Enter to get one by email: ")
	line, err := reader.ReadString('\n')
	if code := strings.TrimSpace(line); code != "" {
		return code, nil
	}
	if err != nil {
		return "", errors.New("a verification code is required: pass --code, or run this command in a terminal")
	}
	token, err := solveChallenge(ctx, api, "dangerous_action_request_otp")
	if err != nil {
		return "", err
	}
	body := map[string]string{"action": action, "target_type": targetType, "target_id": targetID}
	if err := api.DoWithHeaders(ctx, http.MethodPost, "/auth/dangerous-action/request-otp", body, map[string]string{"X-Challenge-Token": token}, nil); err != nil {
		return "", err
	}
	a.printf("We emailed you a verification code. Enter it: ")
	line, _ = reader.ReadString('\n')
	if code := strings.TrimSpace(line); code != "" {
		return code, nil
	}
	return "", errors.New("no verification code entered")
}

// solveChallenge gets a proof-of-work token for action: the leading
// difficulty bits of sha256(data + nonce) must be zero.
func solveChallenge(ctx context.Context, api *devapi.Client, action string) (string, error) {
	var challenge struct {
		ID         string `json:"id"`
		Difficulty int    `json:"difficulty"`
		Data       string `json:"data"`
	}
	if err := api.Do(ctx, http.MethodGet, "/challenge?action="+url.QueryEscape(action), nil, &challenge); err != nil {
		return "", err
	}
	if challenge.Difficulty < 0 || challenge.Difficulty > 256 {
		return "", fmt.Errorf("the API returned an unusable challenge")
	}
	solution := ""
	for nonce := 0; ; nonce++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		candidate := strconv.Itoa(nonce)
		if leadingZeroBits(sha256.Sum256([]byte(challenge.Data+candidate)), challenge.Difficulty) {
			solution = candidate
			break
		}
	}
	var verified struct {
		Token string `json:"token"`
	}
	if err := api.Do(ctx, http.MethodPost, "/challenge/verify", map[string]string{"id": challenge.ID, "solution": solution, "action": action}, &verified); err != nil {
		return "", err
	}
	return verified.Token, nil
}

func leadingZeroBits(hash [32]byte, difficulty int) bool {
	for i := 0; difficulty > 0; i++ {
		if difficulty >= 8 {
			if hash[i] != 0 {
				return false
			}
			difficulty -= 8
			continue
		}
		return hash[i]>>(8-difficulty) == 0
	}
	return true
}
