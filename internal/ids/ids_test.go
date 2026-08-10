package ids

import "testing"

func TestServerIDIsDistinctFromTeamID(t *testing.T) {
	var serverID ServerID = "server-1"
	var teamID TeamID = "team-1"

	if string(serverID) == string(teamID) {
		t.Fatal("test IDs must differ")
	}
}
