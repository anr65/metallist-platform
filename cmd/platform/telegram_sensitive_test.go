package main

import "testing"

func TestTelegramSensitiveWithdrawalBoundary(t *testing.T) {
	for _, text := range []string{
		"061234 244700/0 комса 4500\n4321 248к/389",
		"/withdraw 061234 244700/0 комса 4500",
		"/withdraw@synthetic_bot 061234 244700/0;221234 200000/30",
		"1234 12345678/0",
		"061234\t244700/0",
	} {
		if telegramHasSensitiveNumber(text) {
			t.Errorf("valid withdrawal rejected: %q", text)
		}
	}
	for _, text := range []string{
		"4000000000061234", "4000 0000 0006 1234", "4000-0000-0006-1234",
		"/withdraw 4000 0000 0006 1234 100/0",
		"/withdraw 061234 244700/0 комса 4000000000061234",
		"061234 244700/0\n4000 0000 0006 1234",
		"061234 244700/0;4000-0000-0006-1234",
		"061234 4000000000061234/0",
		"061234 244700/4000000000061234",
		"061234 244700",
	} {
		if !telegramHasSensitiveNumber(text) {
			t.Errorf("sensitive number not rejected: %q", text)
		}
	}
}
