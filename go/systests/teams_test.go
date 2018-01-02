package systests

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/net/context"

	"github.com/davecgh/go-spew/spew"
	"github.com/keybase/client/go/client"
	"github.com/keybase/client/go/engine"
	"github.com/keybase/client/go/libkb"
	"github.com/keybase/client/go/protocol/keybase1"
	"github.com/keybase/client/go/teams"
	"github.com/keybase/go-framed-msgpack-rpc/rpc"
	"github.com/stretchr/testify/require"
)

func TestTeamCreate(t *testing.T) {
	tt := newTeamTester(t)
	defer tt.cleanup()

	tt.addUser("onr")
	tt.addUser("wtr")

	team := tt.users[0].createTeam()
	tt.users[0].addTeamMember(team, tt.users[1].username, keybase1.TeamRole_WRITER)
}

func TestTeamBustCache(t *testing.T) {
	tt := newTeamTester(t)
	defer tt.cleanup()

	tt.addUser("onr")
	tt.addUser("adm")
	tt.addUser("wtr")

	team := tt.users[0].createTeam()
	tt.users[0].addTeamMember(team, tt.users[1].username, keybase1.TeamRole_ADMIN)

	before, err := GetTeamForTestByStringName(context.TODO(), tt.users[0].tc.G, team)
	require.NoError(t, err)
	beforeSeqno := before.CurrentSeqno()
	tt.users[1].addTeamMember(team, tt.users[2].username, keybase1.TeamRole_WRITER)

	// Poll for an update, we should get it as soon as gregor tells us to bust our cache.
	backoff := 100 * time.Millisecond
	found := false
	for i := 0; i < 10; i++ {
		after, err := teams.Load(context.TODO(), tt.users[0].tc.G, keybase1.LoadTeamArg{
			Name:    team,
			StaleOK: true,
		})
		require.NoError(t, err)
		if after.CurrentSeqno() > beforeSeqno {
			t.Logf("Found new seqno %d at poll loop iter %d", after.CurrentSeqno(), i)
			found = true
			break
		}
		t.Logf("Still at old generation %d at poll loop iter %d", beforeSeqno, i)
		time.Sleep(backoff)
		backoff += backoff / 2
	}
	require.True(t, found)
}

func TestTeamRotateOnRevoke(t *testing.T) {
	tt := newTeamTester(t)
	defer tt.cleanup()

	tt.addUser("onr")
	tt.addUser("wtr")

	teamID, teamName := tt.users[0].createTeam2()
	tt.users[0].addTeamMember(teamName.String(), tt.users[1].username, keybase1.TeamRole_WRITER)

	// get the before state of the team
	before, err := GetTeamForTestByStringName(context.TODO(), tt.users[0].tc.G, teamName.String())
	if err != nil {
		t.Fatal(err)
	}
	if before.Generation() != 1 {
		t.Errorf("generation before rotate: %d, expected 1", before.Generation())
	}
	secretBefore := before.Data.PerTeamKeySeeds[before.Generation()].Seed.ToBytes()

	// User1 should get a gregor that the team he was just added to changed.
	tt.users[1].waitForTeamChangedGregor(teamID, keybase1.Seqno(2))
	// User0 should get a (redundant) gregor notification that
	// he just changed the team.
	tt.users[0].waitForTeamChangedGregor(teamID, keybase1.Seqno(2))

	tt.users[1].revokePaperKey()
	tt.users[0].waitForRotateByID(teamID, keybase1.Seqno(3))

	// check that key was rotated for team
	after, err := GetTeamForTestByStringName(context.TODO(), tt.users[0].tc.G, teamName.String())
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation() != 2 {
		t.Errorf("generation after rotate: %d, expected 2", after.Generation())
	}
	secretAfter := after.Data.PerTeamKeySeeds[after.Generation()].Seed.ToBytes()
	if libkb.SecureByteArrayEq(secretAfter, secretBefore) {
		t.Fatal("team secret did not change when rotated")
	}
}

type teamTester struct {
	t     *testing.T
	users []*userPlusDevice
}

func newTeamTester(t *testing.T) *teamTester {
	return &teamTester{t: t}
}

func (tt *teamTester) addUser(pre string) *userPlusDevice {
	return tt.addUserHelper(pre, true, true)
}

func (tt *teamTester) addUserNoPaper(pre string) *userPlusDevice {
	return tt.addUserHelper(pre, true, false)
}

func (tt *teamTester) addPuklessUser(pre string) *userPlusDevice {
	return tt.addUserHelper(pre, false, true)
}

func (tt *teamTester) addUserHelper(pre string, puk bool, paper bool) *userPlusDevice {
	tctx := setupTest(tt.t, pre)
	if !puk {
		tctx.Tp.DisableUpgradePerUserKey = true
	}
	var u userPlusDevice
	u.device = &deviceWrapper{tctx: tctx}
	u.device.start(0)

	userInfo := randomUser(pre)
	require.True(tt.t, libkb.CheckUsername.F(userInfo.username), "username check failed (%v): %v", libkb.CheckUsername.Hint, userInfo.username)
	tc := u.device.tctx
	g := tc.G
	signupUI := signupUI{
		info:         userInfo,
		Contextified: libkb.NewContextified(g),
	}
	g.SetUI(&signupUI)
	signup := client.NewCmdSignupRunner(g)
	signup.SetTestWithPaper(paper)
	if err := signup.Run(); err != nil {
		tt.t.Fatal(err)
	}
	tt.t.Logf("signed up %s", userInfo.username)

	u.tc = tc
	u.userInfo = userInfo
	u.username = userInfo.username
	u.passphrase = userInfo.passphrase
	u.uid = libkb.UsernameToUID(u.username)

	cli, xp, err := client.GetRPCClientWithContext(g)
	if err != nil {
		tt.t.Fatal(err)
	}

	u.deviceClient = keybase1.DeviceClient{Cli: cli}
	u.device.userClient = keybase1.UserClient{Cli: cli}

	// register for notifications
	u.notifications = newTeamNotifyHandler()
	srv := rpc.NewServer(xp, nil)
	if err = srv.Register(keybase1.NotifyTeamProtocol(u.notifications)); err != nil {
		tt.t.Fatal(err)
	}
	ncli := keybase1.NotifyCtlClient{Cli: cli}
	if err = ncli.SetNotifications(context.TODO(), keybase1.NotificationChannels{
		Team: true,
	}); err != nil {
		tt.t.Fatal(err)
	}

	u.teamsClient = keybase1.TeamsClient{Cli: cli}

	g.ConfigureConfig()

	devices, backups := u.device.loadEncryptionKIDs()
	require.Len(tt.t, devices, 1, "devices")
	u.device.deviceKey.KID = devices[0]
	require.True(tt.t, u.device.deviceKey.KID.Exists())
	if paper {
		require.Len(tt.t, backups, 1, "backup keys")
		u.backupKey = backups[0]
		u.backupKey.secret = signupUI.info.displayedPaperKey
	} else {
		require.Len(tt.t, backups, 0, "backup keys")
	}

	tt.users = append(tt.users, &u)
	return &u
}

func (tt *teamTester) cleanup() {
	for _, u := range tt.users {
		u.device.tctx.Cleanup()
	}
}

type userPlusDevice struct {
	uid                      keybase1.UID
	username                 string
	passphrase               string
	userInfo                 *signupInfo
	backupKey                backupKey
	device                   *deviceWrapper
	tc                       *libkb.TestContext
	deviceClient             keybase1.DeviceClient
	teamsClient              keybase1.TeamsClient
	notifications            *teamNotifyHandler
	suppressTeamChatAnnounce bool
}

func (u *userPlusDevice) createTeam() string {
	create := client.NewCmdTeamCreateRunner(u.tc.G)
	nameStr, err := libkb.RandString("tt", 5)
	if err != nil {
		u.tc.T.Fatal(err)
	}
	name, err := keybase1.TeamNameFromString(strings.ToLower(nameStr))
	if err != nil {
		u.tc.T.Fatal(err)
	}
	create.TeamName = name
	tracer := u.tc.G.CTimeTracer(context.Background(), "tracer-create-team")
	defer tracer.Finish()
	if err := create.Run(); err != nil {
		u.tc.T.Fatal(err)
	}
	return create.TeamName.String()
}

func (u *userPlusDevice) createTeam2() (teamID keybase1.TeamID, teamName keybase1.TeamName) {
	name := u.createTeam()
	team, err := teams.Load(context.Background(), u.tc.G, keybase1.LoadTeamArg{
		Name: name,
	})
	require.NoError(u.tc.T, err)
	return team.ID, team.Name()
}

func (u *userPlusDevice) teamSetSettings(teamName string, settings keybase1.TeamSettings) {
	err := u.teamsClient.TeamSetSettings(context.Background(), keybase1.TeamSetSettingsArg{
		Name:     teamName,
		Settings: settings,
	})
	require.NoError(u.tc.T, err)
}

func (u *userPlusDevice) teamGetDetails(teamName string) keybase1.TeamDetails {
	res, err := u.teamsClient.TeamGet(context.Background(), keybase1.TeamGetArg{
		Name:        teamName,
		ForceRepoll: true,
	})
	require.NoError(u.tc.T, err)
	return res
}

func (u *userPlusDevice) addTeamMember(team, username string, role keybase1.TeamRole) {
	add := client.NewCmdTeamAddMemberRunner(u.tc.G)
	add.Team = team
	add.Username = username
	add.Role = role
	add.SkipChatNotification = u.suppressTeamChatAnnounce
	if err := add.Run(); err != nil {
		u.tc.T.Fatal(err)
	}
}

func (u *userPlusDevice) leave(team string) {
	leave := client.NewCmdTeamLeaveRunner(u.tc.G)
	leave.Team = team
	err := leave.Run()
	require.NoError(u.tc.T, err)
}

func (u *userPlusDevice) changeTeamMember(team, username string, role keybase1.TeamRole) {
	change := client.NewCmdTeamEditMemberRunner(u.tc.G)
	change.Team = team
	change.Username = username
	change.Role = keybase1.TeamRole_OWNER
	if err := change.Run(); err != nil {
		u.tc.T.Fatal(err)
	}
}

func (u *userPlusDevice) addTeamMemberEmail(team, email string, role keybase1.TeamRole) {
	add := client.NewCmdTeamAddMemberRunner(u.tc.G)
	add.Team = team
	add.Email = email
	add.Role = role
	if err := add.Run(); err != nil {
		u.tc.T.Fatal(err)
	}
}

func (u *userPlusDevice) readInviteEmails(email string) []string {
	arg := libkb.NewAPIArg("test/team/get_tokens")
	arg.Args = libkb.NewHTTPArgs()
	arg.Args.Add("email", libkb.S{Val: email})
	res, err := u.tc.G.API.Get(arg)
	if err != nil {
		u.tc.T.Fatal(err)
	}
	tokens := res.Body.AtKey("tokens")
	n, err := tokens.Len()
	if err != nil {
		u.tc.T.Fatal(err)
	}
	if n == 0 {
		u.tc.T.Fatalf("no invite tokens for %s", email)
	}

	exp := make([]string, n)
	for i := 0; i < n; i++ {
		token, err := tokens.AtIndex(i).GetString()
		if err != nil {
			u.tc.T.Fatal(err)
		}
		exp[i] = token
	}

	return exp
}

func (u *userPlusDevice) acceptEmailInvite(token string) {
	c := client.NewCmdTeamAcceptInviteRunner(u.tc.G)
	c.Token = token
	if err := c.Run(); err != nil {
		u.tc.T.Fatal(err)
	}
}

func (u *userPlusDevice) acceptInviteOrRequestAccess(tokenOrName string) keybase1.TeamAcceptOrRequestResult {
	ret, err := teams.TeamAcceptInviteOrRequestAccess(context.TODO(), u.tc.G, tokenOrName)
	require.NoError(u.tc.T, err)
	return ret
}

func (u *userPlusDevice) teamList(userAssertion string, all, includeImplicitTeams bool) keybase1.AnnotatedTeamList {
	cli := u.teamsClient
	res, err := cli.TeamListUnverified(context.TODO(), keybase1.TeamListUnverifiedArg{
		UserAssertion:        userAssertion,
		IncludeImplicitTeams: includeImplicitTeams,
	})
	require.NoError(u.tc.T, err)
	return res
}

func (u *userPlusDevice) teamListTeammates(includeImplicitTeams bool) keybase1.AnnotatedTeamList {
	cli := u.teamsClient
	res, err := cli.TeamListTeammates(context.TODO(), keybase1.TeamListTeammatesArg{
		IncludeImplicitTeams: includeImplicitTeams,
	})
	require.NoError(u.tc.T, err)
	return res
}

func (u *userPlusDevice) revokePaperKey() {
	id := u.paperKeyID()

	runner := client.NewCmdDeviceRemoveRunner(u.tc.G)
	runner.SetIDOrName(id.String())
	if err := runner.Run(); err != nil {
		u.tc.T.Fatal(err)
	}
}

func (u *userPlusDevice) devices() []keybase1.Device {
	d, err := u.deviceClient.DeviceList(context.TODO(), 0)
	if err != nil {
		u.tc.T.Fatal(err)
	}
	return d
}

func (u *userPlusDevice) userVersion() keybase1.UserVersion {
	uv, err := u.device.userClient.MeUserVersion(context.TODO(), 0)
	require.NoError(u.tc.T, err)
	return uv
}

func (u *userPlusDevice) paperKeyID() keybase1.DeviceID {
	for _, d := range u.devices() {
		if d.Type == libkb.DeviceTypePaper {
			return d.DeviceID
		}
	}
	u.tc.T.Fatal("no paper key found")
	return keybase1.DeviceID("")
}

func (u *userPlusDevice) waitForTeamChangedGregor(teamID keybase1.TeamID, toSeqno keybase1.Seqno) {
	// process 10 team rotations or 10s worth of time
	for i := 0; i < 10; i++ {
		select {
		case arg := <-u.notifications.changeCh:
			u.tc.T.Logf("membership change received: %+v", arg)
			if arg.TeamID.Eq(teamID) && arg.Changes.MembershipChanged && !arg.Changes.KeyRotated && !arg.Changes.Renamed && arg.LatestSeqno == toSeqno {
				u.tc.T.Logf("change matched!")
				return
			}
			u.tc.T.Logf("ignoring change message (expected teamID = %q, seqno = %d)", teamID.String(), toSeqno)
		case <-time.After(1 * time.Second * libkb.CITimeMultiplier(u.tc.G)):
		}
	}
	u.tc.T.Fatalf("timed out waiting for team rotate %s", teamID)
}

func (u *userPlusDevice) drainGregor() {
	for i := 0; i < 1000; i++ {
		select {
		case <-u.notifications.changeCh:
			u.tc.T.Logf("dropped notification")
			// drop
		case <-time.After(500 * time.Millisecond * libkb.CITimeMultiplier(u.tc.G)):
			u.tc.T.Logf("no notification received, drain complete")
			return
		}
	}
}

func (u *userPlusDevice) waitForRotateByID(teamID keybase1.TeamID, toSeqno keybase1.Seqno) {
	u.tc.T.Logf("waiting for team rotate %s", teamID)

	// jump start the clkr queue processing loop
	u.kickTeamRekeyd()

	// process 10 team rotations or 10s worth of time
	for i := 0; i < 10; i++ {
		select {
		case arg := <-u.notifications.changeCh:
			u.tc.T.Logf("rotate received: %+v", arg)
			if arg.TeamID.Eq(teamID) && arg.Changes.KeyRotated && arg.LatestSeqno == toSeqno {
				u.tc.T.Logf("rotate matched!")
				return
			}
			u.tc.T.Logf("ignoring rotate message")
		case <-time.After(1 * time.Second * libkb.CITimeMultiplier(u.tc.G)):
		}
	}
	u.tc.T.Fatalf("timed out waiting for team rotate %s", teamID)
}

func (u *userPlusDevice) waitForTeamChangedAndRotated(teamID keybase1.TeamID, toSeqno keybase1.Seqno) {
	// process 10 team rotations or 10s worth of time
	for i := 0; i < 10; i++ {
		select {
		case arg := <-u.notifications.changeCh:
			u.tc.T.Logf("membership change received: %+v", arg)
			if arg.TeamID.Eq(teamID) && arg.Changes.MembershipChanged && arg.Changes.KeyRotated && !arg.Changes.Renamed && arg.LatestSeqno == toSeqno {
				u.tc.T.Logf("change matched!")
				return
			}
			u.tc.T.Logf("ignoring change message (expected team = %v, seqno = %d)", teamID, toSeqno)
		case <-time.After(1 * time.Second * libkb.CITimeMultiplier(u.tc.G)):
		}
	}
	u.tc.T.Fatalf("timed out waiting for team rotate %s", teamID)
}

func (u *userPlusDevice) pollForTeamSeqnoLink(team string, toSeqno keybase1.Seqno) {
	for i := 0; i < 20; i++ {
		after, err := teams.Load(context.TODO(), u.tc.G, keybase1.LoadTeamArg{
			Name:        team,
			ForceRepoll: true,
		})
		if err != nil {
			u.tc.T.Fatalf("error while loading team %q: %v", team, err)
		}

		if after.CurrentSeqno() >= toSeqno {
			u.tc.T.Logf("Found new seqno %d at poll loop iter %d", after.CurrentSeqno(), i)
			return
		}

		time.Sleep(500 * time.Millisecond)
	}

	u.tc.T.Fatalf("timed out waiting for team rotate %s", team)
}

func (u *userPlusDevice) proveRooter() {
	cmd := client.NewCmdProveRooterRunner(u.tc.G, u.username)
	if err := cmd.Run(); err != nil {
		u.tc.T.Fatal(err)
	}
}

func (u *userPlusDevice) track(username string) {
	trackCmd := client.NewCmdTrackRunner(u.tc.G)
	trackCmd.SetUser(username)
	trackCmd.SetOptions(keybase1.TrackOptions{BypassConfirm: true})
	err := trackCmd.Run()
	require.NoError(u.tc.T, err)
}

func (u *userPlusDevice) getTeamSeqno(teamID keybase1.TeamID) keybase1.Seqno {
	team, err := teams.Load(context.Background(), u.tc.G, keybase1.LoadTeamArg{
		ID:          teamID,
		Public:      teamID.IsPublic(),
		ForceRepoll: true,
	})
	require.NoError(u.tc.T, err)
	return team.CurrentSeqno()
}

func (u *userPlusDevice) kickTeamRekeyd() {
	kickTeamRekeyd(u.tc.G, u.tc.T)
}

func (u *userPlusDevice) lookupImplicitTeam(create bool, displayName string, public bool) (keybase1.TeamID, error) {
	res, err := u.lookupImplicitTeam2(create, displayName, public)
	return res.TeamID, err
}

func (u *userPlusDevice) lookupImplicitTeam2(create bool, displayName string, public bool) (keybase1.LookupImplicitTeamRes, error) {
	cli := u.teamsClient
	var err error
	var res keybase1.LookupImplicitTeamRes
	if create {
		res, err = cli.LookupOrCreateImplicitTeam(context.TODO(), keybase1.LookupOrCreateImplicitTeamArg{Name: displayName, Public: public})
	} else {
		res, err = cli.LookupImplicitTeam(context.TODO(), keybase1.LookupImplicitTeamArg{Name: displayName, Public: public})
	}
	return res, err
}

func (u *userPlusDevice) newSecretUI() *libkb.TestSecretUI {
	return &libkb.TestSecretUI{Passphrase: u.passphrase}
}

func (u *userPlusDevice) provisionNewDevice() *deviceWrapper {
	tc := setupTest(u.tc.T, "sub")
	t := tc.T
	g := tc.G

	device := &deviceWrapper{tctx: tc}
	device.start(0)

	// ui for provisioning
	ui := &rekeyProvisionUI{username: u.username, backupKey: u.backupKey}
	{
		_, xp, err := client.GetRPCClientWithContext(g)
		require.NoError(t, err)
		srv := rpc.NewServer(xp, nil)
		protocols := []rpc.Protocol{
			keybase1.LoginUiProtocol(ui),
			keybase1.SecretUiProtocol(ui),
			keybase1.ProvisionUiProtocol(ui),
		}
		for _, prot := range protocols {
			err = srv.Register(prot)
			require.NoError(t, err)
		}
	}

	cmd := client.NewCmdLoginRunner(g)
	err := cmd.Run()
	require.NoError(t, err, "login")

	// Clear the paper key.
	err = g.LoginState().Account(func(a *libkb.Account) {
		a.ClearPaperKeys()
	}, "provisionNewDevice")
	require.NoError(t, err, "clear paper key")

	skey, err := g.ActiveDevice.SigningKey()
	require.NoError(t, err)
	device.deviceKey.KID = skey.GetKID()
	require.True(t, device.deviceKey.KID.Exists())

	return device
}

func (u *userPlusDevice) reset() {
	uvBefore := u.userVersion()
	err := u.device.userClient.ResetUser(context.TODO(), 0)
	require.NoError(u.tc.T, err)
	uvAfter := u.userVersion()
	require.NotEqual(u.tc.T, uvBefore.EldestSeqno, uvAfter.EldestSeqno,
		"eldest seqno should change as result of reset")
}

func (u *userPlusDevice) loginAfterReset() {
	u.loginAfterResetHelper(true)
}

func (u *userPlusDevice) loginAfterResetPukless() {
	u.loginAfterResetHelper(false)
}

func (u *userPlusDevice) loginAfterResetHelper(puk bool) {
	t := u.device.tctx.T
	u.device.tctx.Tp.DisableUpgradePerUserKey = !puk
	g := u.device.tctx.G

	ui := genericUI{
		g:           g,
		SecretUI:    signupInfoSecretUI{u.userInfo, u.tc.G.GetLog()},
		LoginUI:     usernameLoginUI{u.username},
		ProvisionUI: nullProvisionUI{randomDevice()},
	}
	g.SetUI(&ui)
	loginCmd := client.NewCmdLoginRunner(g)
	loginCmd.Username = u.username
	err := loginCmd.Run()
	require.NoError(t, err, "login after reset")
}

func kickTeamRekeyd(g *libkb.GlobalContext, t libkb.TestingTB) {
	apiArg := libkb.APIArg{
		Endpoint:    "test/accelerate_team_rekeyd",
		Args:        libkb.HTTPArgs{},
		SessionType: libkb.APISessionTypeREQUIRED,
	}

	_, err := g.API.Post(apiArg)
	if err != nil {
		t.Fatalf("Failed to accelerate team rekeyd: %s", err)
	}
}

func GetTeamForTestByStringName(ctx context.Context, g *libkb.GlobalContext, name string) (*teams.Team, error) {
	return teams.Load(ctx, g, keybase1.LoadTeamArg{
		Name:        name,
		ForceRepoll: true,
	})
}

func GetTeamForTestByID(ctx context.Context, g *libkb.GlobalContext, id keybase1.TeamID, public bool) (*teams.Team, error) {
	return teams.Load(ctx, g, keybase1.LoadTeamArg{
		ID:          id,
		Public:      public,
		ForceRepoll: true,
	})
}

type teamNotifyHandler struct {
	changeCh chan keybase1.TeamChangedByIDArg
}

func newTeamNotifyHandler() *teamNotifyHandler {
	return &teamNotifyHandler{
		changeCh: make(chan keybase1.TeamChangedByIDArg, 1),
	}
}

func (n *teamNotifyHandler) TeamChangedByID(ctx context.Context, arg keybase1.TeamChangedByIDArg) error {
	n.changeCh <- arg
	return nil
}

func (n *teamNotifyHandler) TeamChangedByName(ctx context.Context, arg keybase1.TeamChangedByNameArg) error {
	return nil
}

func (n *teamNotifyHandler) TeamDeleted(ctx context.Context, teamID keybase1.TeamID) error {
	return nil
}

func (n *teamNotifyHandler) TeamExit(ctx context.Context, teamID keybase1.TeamID) error {
	return nil
}

func TestGetTeamRootID(t *testing.T) {
	tt := newTeamTester(t)
	defer tt.cleanup()

	tt.addUser("onr")

	t.Logf("create a team")
	parentName, err := keybase1.TeamNameFromString(tt.users[0].createTeam())
	require.NoError(t, err)

	parentID := parentName.ToPrivateTeamID()

	t.Logf("create a subteam")
	subteamID, err := teams.CreateSubteam(context.TODO(), tt.users[0].tc.G, "mysubteam", parentName)
	require.NoError(t, err)

	subteamName, err := parentName.Append("mysubteam")
	require.NoError(t, err)

	t.Logf("create a sub-subteam")
	subteamID2, err := teams.CreateSubteam(context.TODO(), tt.users[0].tc.G, "teamofsubs", subteamName)
	require.NoError(t, err)

	getAndCompare := func(id keybase1.TeamID) {
		retID, err := teams.GetRootID(context.TODO(), tt.users[0].tc.G, id)
		require.NoError(t, err)
		require.Equal(t, parentID, retID)
	}

	getAndCompare(*subteamID)
	getAndCompare(*subteamID2)
	getAndCompare(parentID)
}

// Test that we can still load a valid link a signed by a now-revoked device.
func TestTeamSignedByRevokedDevice(t *testing.T) {
	tt := newTeamTester(t)
	defer tt.cleanup()

	// the signer
	alice := tt.addUser("alice")

	// the loader
	bob := tt.addUser("bob")

	teamName := alice.createTeam()
	alice.addTeamMember(teamName, bob.username, keybase1.TeamRole_ADMIN)

	t.Logf("alice revokes the device used to sign team links")
	var revokedKID keybase1.KID
	{
		devices, _ := getActiveDevicesAndKeys(alice.tc, alice.username)
		var target *libkb.Device
		for _, device := range devices {
			if device.Type != libkb.DeviceTypePaper {
				target = device
			}
		}
		require.NotNil(t, target)
		revokedKID = target.Kid

		revokeEngine := engine.NewRevokeDeviceEngine(engine.RevokeDeviceEngineArgs{
			ID:        target.ID,
			ForceSelf: true,
			ForceLast: false,
		}, alice.tc.G)
		ectx := &engine.Context{
			LogUI:    alice.tc.G.Log,
			SecretUI: alice.newSecretUI(),
		}
		err := engine.RunEngine(revokeEngine, ectx)
		require.NoError(t, err)
	}

	t.Logf("bob updates cache of alice's info")
	{
		arg := libkb.NewLoadUserArg(bob.tc.G).WithUID(alice.uid).WithPublicKeyOptional().WithForcePoll(true)
		_, _, err := bob.tc.G.GetUPAKLoader().LoadV2(arg)
		require.NoError(t, err)
	}

	t.Logf("bob should see alice's key is revoked")
	{
		_, pubKey, _, err := bob.tc.G.GetUPAKLoader().LoadKeyV2(context.TODO(), alice.uid, revokedKID)
		require.NoError(t, err)
		t.Logf("%v", spew.Sdump(pubKey))
		require.NotNil(t, pubKey.Base.Revocation, "key should be revoked: %v", revokedKID)
	}

	t.Logf("bob loads the team")
	_, err := teams.Load(context.TODO(), bob.tc.G, keybase1.LoadTeamArg{
		Name:            teamName,
		ForceRepoll:     true,
		ForceFullReload: true, // don't use the cache
	})
	require.NoError(t, err)
}

// Another test of loading a team with a valid link signed by a now-revoked device.
// The previous test didn't catch a bug.
// In this test at the time when the device is revoked the team sigchain points to
// a link that was signed by a never-revoked device and is subsequent to the link
// signed by the revoked device.
func TestTeamSignedByRevokedDevice2(t *testing.T) {
	tt := newTeamTester(t)
	defer tt.cleanup()

	// the signer
	alice := tt.addUser("alice")
	aliced2 := alice.provisionNewDevice()

	// the loader
	bob := tt.addUser("bob")

	teamName := alice.createTeam()

	t.Logf("sign a link with the to-be-revoked device (aliced2)")
	{
		eng := client.NewCmdTeamAddMemberRunner(aliced2.tctx.G)
		eng.Team = teamName
		eng.Username = bob.username
		eng.Role = keybase1.TeamRole_ADMIN
		err := eng.Run()
		require.NoError(t, err)
	}

	alice.changeTeamMember(teamName, bob.username, keybase1.TeamRole_ADMIN)

	t.Logf("alice revokes a device used to sign team links (alice2)")
	revokedKID := aliced2.KID()
	require.True(t, revokedKID.Exists())
	{
		devices, _ := getActiveDevicesAndKeys(alice.tc, alice.username)
		var target *libkb.Device
		for _, device := range devices {
			t.Logf("scan device: ID:%v KID:%v", device.ID, device.Kid)
			if device.Kid.Equal(revokedKID) {
				target = device
			}
		}
		require.NotNil(t, target)

		revokeEngine := engine.NewRevokeDeviceEngine(engine.RevokeDeviceEngineArgs{
			ID:        target.ID,
			ForceSelf: true,
			ForceLast: false,
		}, alice.tc.G)
		ectx := &engine.Context{
			LogUI:    alice.tc.G.Log,
			SecretUI: alice.newSecretUI(),
		}
		err := engine.RunEngine(revokeEngine, ectx)
		require.NoError(t, err)
	}

	t.Logf("bob updates cache of alice's info")
	{
		arg := libkb.NewLoadUserArg(bob.tc.G).WithUID(alice.uid).WithPublicKeyOptional().WithForcePoll(true)
		_, _, err := bob.tc.G.GetUPAKLoader().LoadV2(arg)
		require.NoError(t, err)
	}

	t.Logf("bob should see alice's key is revoked")
	{
		_, pubKey, _, err := bob.tc.G.GetUPAKLoader().LoadKeyV2(context.TODO(), alice.uid, revokedKID)
		require.NoError(t, err)
		t.Logf("%v", spew.Sdump(pubKey))
		require.NotNil(t, pubKey.Base.Revocation, "key should be revoked: %v", revokedKID)
	}

	t.Logf("bob loads the team")
	_, err := teams.Load(context.TODO(), bob.tc.G, keybase1.LoadTeamArg{
		Name:            teamName,
		ForceRepoll:     true,
		ForceFullReload: true, // don't use the cache
	})
	require.NoError(t, err)
}

func TestImpTeamLookupWithTrackingFailure(t *testing.T) {
	tt := newTeamTester(t)
	defer tt.cleanup()

	alice := tt.addUser("alice")
	g := tt.users[0].tc.G

	tt.addUser("wong")
	wong := tt.users[1]

	iTeamNameCreate := strings.Join([]string{alice.username, wong.username}, ",")

	t.Logf("make an implicit team")
	team, err := alice.lookupImplicitTeam(true /*create*/, iTeamNameCreate, false /*isPublic*/)
	require.NoError(t, err)

	iui := newSimpleIdentifyUI()
	attachIdentifyUI(t, g, iui)

	t.Logf("prove rooter and track")
	g.ProofCache.DisableDisk()
	wong.proveRooter()
	iui.confirmRes = keybase1.ConfirmResult{IdentityConfirmed: true, RemoteConfirmed: true, AutoConfirmed: true}
	tt.users[0].track(wong.username)
	iui.confirmRes = keybase1.ConfirmResult{}

	t.Logf("make rooter unreachable")
	g.XAPI = &flakeyRooterAPI{orig: g.XAPI, hardFail: true, G: g}
	g.ProofCache.Reset()

	t.Logf("lookup the implicit team while full identify is failing")
	team2, err := alice.lookupImplicitTeam(true /*create*/, iTeamNameCreate, false /*isPublic*/)
	require.NoError(t, err)
	require.Equal(t, team, team2)
}

// Leave a team and make sure the team list no longer includes it.
func TestTeamLeaveThenList(t *testing.T) {
	tt := newTeamTester(t)
	defer tt.cleanup()

	alice := tt.addUser("alice")
	bob := tt.addUser("bob")

	teamID, teamName := alice.createTeam2()
	// add bob as owner because we can't leave as the only owner.
	alice.addTeamMember(teamName.String(), bob.username, keybase1.TeamRole_OWNER)

	teams := alice.teamList("", false, false)
	require.Len(t, teams.Teams, 1)
	require.Equal(t, teamID, teams.Teams[0].TeamID)

	alice.leave(teamName.String())

	teams = alice.teamList("", false, false)
	require.Len(t, teams.Teams, 0)
}

func TestTeamCanUserPerform(t *testing.T) {
	tt := newTeamTester(t)
	defer tt.cleanup()

	ann := tt.addUser("ann")
	bob := tt.addUser("bob")
	pam := tt.addUser("pam")
	edd := tt.addUser("edd")

	team := ann.createTeam()
	ann.addTeamMember(team, bob.username, keybase1.TeamRole_ADMIN)
	ann.addTeamMember(team, pam.username, keybase1.TeamRole_WRITER)
	ann.addTeamMember(team, edd.username, keybase1.TeamRole_READER)

	parentName, err := keybase1.TeamNameFromString(team)
	require.NoError(t, err)

	_, err = teams.CreateSubteam(context.TODO(), ann.tc.G, "mysubteam", parentName)
	require.NoError(t, err)
	subteam := team + ".mysubteam"

	callCanPerform := func(user *userPlusDevice, teamname string) keybase1.TeamOperation {
		ret, err := teams.CanUserPerform(context.TODO(), user.tc.G, teamname)
		t.Logf("teams.CanUserPerform(%s,%s)", user.username, teamname)
		require.NoError(t, err)
		return ret
	}
	annPerms := callCanPerform(ann, team)
	bobPerms := callCanPerform(bob, team)
	pamPerms := callCanPerform(pam, team)
	eddPerms := callCanPerform(edd, team)

	// All ops except leave should be fine for owners and admins
	require.True(t, annPerms.ManageMembers)
	require.True(t, annPerms.ManageSubteams)
	require.True(t, annPerms.CreateChannel)
	require.True(t, annPerms.DeleteChannel)
	require.True(t, annPerms.RenameChannel)
	require.True(t, annPerms.EditChannelDescription)
	require.True(t, annPerms.SetTeamShowcase)
	require.True(t, annPerms.SetMemberShowcase)
	require.True(t, annPerms.ChangeOpenTeam)
	require.False(t, annPerms.LeaveTeam) // sole owner can't leave
	require.True(t, annPerms.ChangeTarsEnabled)

	require.True(t, bobPerms.ManageMembers)
	require.True(t, bobPerms.ManageSubteams)
	require.True(t, bobPerms.CreateChannel)
	require.True(t, bobPerms.DeleteChannel)
	require.True(t, bobPerms.RenameChannel)
	require.True(t, bobPerms.EditChannelDescription)
	require.True(t, bobPerms.SetTeamShowcase)
	require.True(t, bobPerms.SetMemberShowcase)
	require.True(t, bobPerms.ChangeOpenTeam)
	require.True(t, bobPerms.LeaveTeam)
	require.True(t, bobPerms.ChangeTarsEnabled)

	// Some ops are fine for writers
	require.False(t, pamPerms.ManageMembers)
	require.False(t, pamPerms.ManageSubteams)
	require.True(t, pamPerms.CreateChannel)
	require.False(t, pamPerms.DeleteChannel)
	require.False(t, pamPerms.RenameChannel)
	require.False(t, pamPerms.EditChannelDescription)
	require.False(t, pamPerms.SetTeamShowcase)
	require.True(t, pamPerms.SetMemberShowcase)
	require.False(t, pamPerms.ChangeOpenTeam)
	require.True(t, pamPerms.LeaveTeam)
	require.False(t, pamPerms.ChangeTarsEnabled)

	// Only SetMemberShowcase (by default) and LeaveTeam is available for readers
	require.False(t, eddPerms.ManageMembers)
	require.False(t, eddPerms.ManageSubteams)
	require.False(t, eddPerms.CreateChannel)
	require.False(t, eddPerms.DeleteChannel)
	require.False(t, eddPerms.RenameChannel)
	require.False(t, eddPerms.EditChannelDescription)
	require.False(t, eddPerms.SetTeamShowcase)
	require.True(t, eddPerms.SetMemberShowcase)
	require.False(t, eddPerms.ChangeOpenTeam)
	require.True(t, eddPerms.LeaveTeam)
	require.False(t, eddPerms.ChangeTarsEnabled)

	annPerms = callCanPerform(ann, subteam)
	bobPerms = callCanPerform(bob, subteam)

	// Some ops are fine for implicit admins
	require.True(t, annPerms.ManageMembers)
	require.True(t, annPerms.ManageSubteams)
	require.False(t, annPerms.CreateChannel)
	require.False(t, annPerms.DeleteChannel)
	require.False(t, annPerms.RenameChannel)
	require.False(t, annPerms.EditChannelDescription)
	require.True(t, annPerms.SetTeamShowcase)
	require.False(t, annPerms.SetMemberShowcase)
	require.True(t, annPerms.ChangeOpenTeam) // not a member of the subteam
	require.True(t, annPerms.ChangeTarsEnabled)

	require.True(t, bobPerms.ManageMembers)
	require.True(t, bobPerms.ManageSubteams)
	require.False(t, bobPerms.CreateChannel)
	require.False(t, bobPerms.DeleteChannel)
	require.False(t, bobPerms.RenameChannel)
	require.False(t, bobPerms.EditChannelDescription)
	require.True(t, bobPerms.SetTeamShowcase)
	require.False(t, bobPerms.SetMemberShowcase)
	require.True(t, bobPerms.ChangeOpenTeam)
	require.False(t, bobPerms.LeaveTeam) // not a member of the subteam
	require.True(t, annPerms.ChangeTarsEnabled)

	// Invalid team for pam
	_, err = teams.CanUserPerform(context.TODO(), pam.tc.G, subteam)
	require.Error(t, err)
}
