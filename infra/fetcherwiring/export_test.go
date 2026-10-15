package fetcherwiring

// InvalidYouTubeChannelIDForTest exposes invalidYouTubeChannelID to fetcherwiring_test.
func InvalidYouTubeChannelIDForTest(channelID string) (string, bool) {
	return invalidYouTubeChannelID(channelID)
}
