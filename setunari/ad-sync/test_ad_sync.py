# SPDX-FileCopyrightText: 2026 Setunari Group LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
# Run: python3 -m unittest setunari/ad-sync/test_ad_sync.py  (needs ldap3)

import datetime as dt
import io
import json
import os
import sys
import tempfile
import threading
import unittest
import unittest.mock
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import ad_sync  # noqa: E402

NOW = dt.datetime(2026, 11, 18, tzinfo=dt.timezone.utc)


def filetime(when: dt.datetime) -> str:
    return str(int((when - ad_sync.FILETIME_EPOCH).total_seconds() * 10_000_000))


def cfg(**kw):
    base = dict(ldap_url="ldap://x", bind_dn="cn=x", bind_password="p", base_dn="dc=corp,dc=local",
                chatto_url="http://chat", chatto_token="t", state_file=kw.pop("state_file", "/nonexistent"))
    base.update(kw)
    return ad_sync.Config(**base)


class ClassifyTest(unittest.TestCase):
    def test_rules(self):
        c = cfg(exclude_ous=["OU=Former,DC=corp,DC=local"])
        def k(dn, uac="512", exp="0", members=None):
            return ad_sync.classify(dn, {"sAMAccountName": ["Bat"], "userAccountControl": [uac],
                                         "accountExpires": [exp], "mail": ["Bat@Corp.mn"]}, c, members, NOW)
        a = k("CN=Bat,OU=Staff,DC=corp,DC=local")
        self.assertTrue(a.current)
        self.assertEqual(a.login, "bat")
        self.assertEqual(a.emails, {"bat@corp.mn"})
        self.assertEqual(k("CN=Bat,OU=Staff,DC=corp,DC=local", uac="514").reason, "disabled in AD")
        self.assertEqual(k("CN=Bat,OU=Staff,DC=corp,DC=local", exp=filetime(NOW - dt.timedelta(days=1))).reason, "expired in AD")
        self.assertTrue(k("CN=Bat,OU=Staff,DC=corp,DC=local", exp=filetime(NOW + dt.timedelta(days=1))).current)
        self.assertTrue(k("CN=Bat,OU=Staff,DC=corp,DC=local", exp=str(0x7FFFFFFFFFFFFFFF)).current)
        self.assertEqual(k("CN=Bat,OU=Former, DC=corp,DC=local").reason, "in excluded OU")
        self.assertEqual(k("CN=Bat,OU=Staff,DC=corp,DC=local", members=set()).reason, "not in employee group")
        self.assertTrue(k("CN=Bat,OU=Staff,DC=corp,DC=local", members={"cn=bat,ou=staff,dc=corp,dc=local"}).current)


def ad(login, current=True, emails=()):
    return ad_sync.ADAccount(dn=f"cn={login}", login=login, emails=set(emails), current=current,
                             reason="" if current else "disabled in AD")


def user(uid, login, deactivated=False, emails=(), can=True):
    return ad_sync.ChattoUser(uid, login, set(emails), deactivated, can)


class PlanTest(unittest.TestCase):
    def test_plan(self):
        accounts = [ad("bat"), ad("dorj", False), ad("saraa", False, ["saraa@corp.mn"]), ad("tuul"), ad("old", False)]
        users = [
            user("u1", "bat"),
            user("u2", "dorj"),
            user("u3", "saraa.b", emails=["saraa@corp.mn"]),  # matched by email
            user("u4", "tuul", deactivated=True),              # we deactivated earlier
            user("u5", "admin"),                               # no AD account
            user("u6", "old", deactivated=True),               # already deactivated
            user("u7", "manual", deactivated=True),            # no AD, manual deactivation
        ]
        actions, notes = ad_sync.plan(cfg(), accounts, users, ours={"u4"})
        got = {(a.user.login, a.deactivate) for a in actions}
        self.assertEqual(got, {("dorj", True), ("saraa.b", True), ("tuul", False)})
        self.assertEqual(notes, ["admin: no AD account, left unchanged"])

    def test_reactivate_only_ours(self):
        actions, _ = ad_sync.plan(cfg(), [ad("bat")], [user("u1", "bat", deactivated=True)], ours=set())
        self.assertEqual(actions, [])

    def test_unmatched_and_keep(self):
        c = cfg(unmatched="deactivate", keep_logins={"admin"})
        actions, _ = ad_sync.plan(c, [ad("bat")], [user("u1", "admin"), user("u2", "test")], ours=set())
        self.assertEqual([(a.user.login, a.reason) for a in actions], [("test", "no AD account")])


class FakeChatto(BaseHTTPRequestHandler):
    members = {}
    calls = []

    def log_message(self, *a):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        method = self.path.rsplit("/", 1)[1]
        FakeChatto.calls.append((self.path, self.headers.get("Authorization"), body))
        ids = list(self.members)
        if method == "ListMembers":
            off, lim = body["page"]["offset"], body["page"]["limit"]
            resp = {"userIds": ids[off:off + 2], "page": {"totalCount": str(len(ids)), "hasMore": off + 2 < len(ids)}}
        elif method == "BatchGetMembers":
            resp = {"members": [self.members[i] for i in body["userIds"]]}
        elif method == "SetUserDeactivated":
            m = self.members[body["userId"]]
            m["deactivated"] = body["deactivated"]
            resp = {"member": m}
        else:
            self.send_response(404)
            self.end_headers()
            return
        data = json.dumps(resp).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def member(uid, login, **kw):
    m = {"user": {"id": uid, "login": login}, "verifiedEmails": [], "viewerCanChangeDeactivation": True}
    for k, v in kw.items():
        if k in ("bot", "deleted"):
            m["user"][k] = v
        else:
            m[k] = v
    return m


class EndToEndTest(unittest.TestCase):
    def setUp(self):
        FakeChatto.calls = []
        FakeChatto.members = {
            "u1": member("u1", "Bat"),
            "u2": member("u2", "dorj"),
            "u3": member("u3", "helper", bot={"ownerUserId": "u1"}),
            "u4": member("u4", "tuul", deactivated=True),
            "u5": member("u5", "owner", viewerCanChangeDeactivation=False),
        }
        self.srv = HTTPServer(("127.0.0.1", 0), FakeChatto)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.url = f"http://127.0.0.1:{self.srv.server_port}"
        self.tmp = tempfile.TemporaryDirectory()
        self.state = os.path.join(self.tmp.name, "state.json")

    def tearDown(self):
        self.srv.shutdown()
        self.srv.server_close()
        self.tmp.cleanup()

    def test_dry_run_then_apply_then_return(self):
        c = cfg(chatto_url=self.url, state_file=self.state)
        chatto = ad_sync.Chatto(self.url, "cht_BK_x")
        accounts = [ad("bat"), ad("dorj", False), ad("owner", False), ad("tuul")]

        out = io.StringIO()
        self.assertEqual(ad_sync.run(c, False, False, accounts, chatto, out), 0)
        self.assertIn("deactivate dorj", out.getvalue())
        self.assertIn("deactivate owner", out.getvalue())
        self.assertIn("skipped", out.getvalue())
        self.assertNotIn("helper", out.getvalue())
        self.assertFalse(any(p.endswith("SetUserDeactivated") for p, _, _ in FakeChatto.calls))
        self.assertTrue(all(a == "Bearer cht_BK_x" for _, a, _ in FakeChatto.calls))
        self.assertTrue(FakeChatto.calls[0][0].startswith("/api/connect/chatto.admin.v1.AdminUserService/"))

        self.assertEqual(ad_sync.run(c, True, False, accounts, chatto, io.StringIO()), 0)
        self.assertTrue(FakeChatto.members["u2"]["deactivated"])
        self.assertNotIn("deactivated", FakeChatto.members["u5"])
        self.assertEqual(ad_sync.load_state(self.state), {"u2"})

        # tuul was deactivated by hand, not by the sync: stays deactivated.
        self.assertTrue(FakeChatto.members["u4"]["deactivated"])

        # dorj returns to work.
        accounts[1] = ad("dorj")
        ad_sync.run(c, True, False, accounts, chatto, io.StringIO())
        self.assertFalse(FakeChatto.members["u2"]["deactivated"])
        self.assertEqual(ad_sync.load_state(self.state), set())

    def test_safety_limits(self):
        chatto = ad_sync.Chatto(self.url, "t")
        with self.assertRaises(ad_sync.SyncError):
            ad_sync.run(cfg(state_file=self.state), True, False, [ad("x", False)], chatto, io.StringIO())
        c = cfg(state_file=self.state, max_deactivations=1)
        accounts = [ad("bat", False), ad("dorj", False), ad("someone")]
        with self.assertRaises(ad_sync.SyncError):
            ad_sync.run(c, True, False, accounts, chatto, io.StringIO())
        self.assertFalse(any(p.endswith("SetUserDeactivated") for p, _, _ in FakeChatto.calls))
        ad_sync.run(c, True, True, accounts, chatto, io.StringIO())
        self.assertTrue(FakeChatto.members["u1"]["deactivated"])


class LdapMockTest(unittest.TestCase):
    def test_read_ad_with_mock_directory(self):
        import ldap3

        server = ldap3.Server("mock", get_info=ldap3.NONE)
        conn = ldap3.Connection(server, user="cn=svc,dc=corp,dc=local", password="p", client_strategy=ldap3.MOCK_SYNC)
        conn.strategy.add_entry("cn=svc,dc=corp,dc=local", {"userPassword": "p"})
        def add(cn, uac):
            conn.strategy.add_entry(f"cn={cn},ou=staff,dc=corp,dc=local", {
                "objectClass": ["top", "person", "user"], "objectCategory": "person",
                "sAMAccountName": cn, "userAccountControl": uac, "accountExpires": "0",
                "mail": f"{cn}@corp.mn"})
        add("bat", "512")
        add("dorj", "514")
        conn.bind()
        accounts = ad_sync.read_ad(cfg(), connection=conn, now=NOW)
        self.assertEqual({(a.login, a.current) for a in accounts}, {("bat", True), ("dorj", False)})


class ConfigTest(unittest.TestCase):
    def test_env(self):
        env = {"AD_LDAP_URL": "ldaps://dc1", "AD_BIND_DN": "cn=svc", "AD_BIND_PASSWORD": "p",
               "AD_BASE_DN": "dc=corp", "CHATTO_URL": "https://chat/", "CHATTO_TOKEN": "t",
               "AD_EXCLUDE_OUS": "OU=A,DC=corp; OU=B,DC=corp", "ADSYNC_KEEP_LOGINS": "Admin, root"}
        c = ad_sync.config_from_env(env)
        self.assertEqual(c.exclude_ous, ["OU=A,DC=corp", "OU=B,DC=corp"])
        self.assertEqual(c.keep_logins, {"admin", "root"})
        self.assertEqual(c.chatto_url, "https://chat")
        with self.assertRaises(ad_sync.SyncError):
            ad_sync.config_from_env({**env, "ADSYNC_UNMATCHED": "delete"})


class MatchingTest(unittest.TestCase):
    """Users can change their own Chatto login, so verified emails win."""

    def test_renamed_login_does_not_hide_a_former_employee(self):
        # The leaver renamed their Chatto login to a current employee's
        # sAMAccountName. Their verified email still points to their own
        # disabled AD account.
        accounts = [ad("bat"), ad("dorj", False, ["dorj@corp.mn"])]
        users = [user("u1", "bat", emails=["dorj@corp.mn"])]
        actions, notes = ad_sync.plan(cfg(), accounts, users, ours=set())
        self.assertEqual([(a.user.id, a.deactivate) for a in actions], [("u1", True)])
        self.assertEqual(len(notes), 1)
        self.assertIn("different AD account", notes[0])

    def test_login_collision_does_not_deactivate_a_current_employee(self):
        # The Chatto login equals a former employee's sAMAccountName, but the
        # verified email belongs to a current employee.
        accounts = [ad("old", False), ad("saraa", True, ["saraa@corp.mn"])]
        users = [user("u1", "old", emails=["saraa@corp.mn"])]
        actions, _ = ad_sync.plan(cfg(), accounts, users, ours=set())
        self.assertEqual(actions, [])

    def test_login_is_the_fallback_without_a_matching_email(self):
        accounts = [ad("dorj", False, ["dorj@corp.mn"])]
        users = [user("u1", "dorj", emails=["private@example.com"])]
        actions, notes = ad_sync.plan(cfg(), accounts, users, ours=set())
        self.assertEqual([(a.user.id, a.deactivate) for a in actions], [("u1", True)])
        self.assertEqual(notes, [])

    def test_login_and_email_of_the_same_account_give_no_note(self):
        accounts = [ad("dorj", False, ["dorj@corp.mn"])]
        users = [user("u1", "dorj", emails=["dorj@corp.mn"])]
        _, notes = ad_sync.plan(cfg(), accounts, users, ours=set())
        self.assertEqual(notes, [])

    def test_any_current_email_match_keeps_the_account(self):
        # Two verified emails: one for an old disabled account, one for the
        # current account (for example after a rehire).
        accounts = [ad("bat.old", False, ["bat.old@corp.mn"]), ad("bat", True, ["bat@corp.mn"])]
        users = [user("u1", "bat", emails=["bat.old@corp.mn", "bat@corp.mn"])]
        actions, _ = ad_sync.plan(cfg(), accounts, users, ours=set())
        self.assertEqual(actions, [])

    def test_keep_logins_is_case_insensitive_on_the_chatto_side(self):
        c = cfg(keep_logins={"admin"})
        # ChattoUser logins are lower case (Chatto.users() lowers them).
        actions, _ = ad_sync.plan(c, [ad("admin", False)], [user("u1", "admin")], ours=set())
        self.assertEqual(actions, [])


class StateTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.state = os.path.join(self.tmp.name, "state.json")

    def tearDown(self):
        self.tmp.cleanup()

    def test_missing_state_file_is_empty(self):
        self.assertEqual(ad_sync.load_state(self.state), set())

    def test_save_and_load(self):
        ad_sync.save_state(self.state, {"u2", "u1"})
        self.assertEqual(ad_sync.load_state(self.state), {"u1", "u2"})
        with open(self.state, encoding="utf-8") as f:
            self.assertEqual(json.load(f), {"deactivated_by_sync": ["u1", "u2"]})
        self.assertFalse(os.path.exists(self.state + ".tmp"))


class _RecordingChatto:
    """In-memory stand-in for ad_sync.Chatto."""

    def __init__(self, users, fail_ids=()):
        self._users = users
        self.fail_ids = set(fail_ids)
        self.calls = []

    def users(self):
        return [ad_sync.ChattoUser(u.id, u.login, set(u.emails), u.deactivated, u.can_change) for u in self._users]

    def set_deactivated(self, user_id, deactivated):
        self.calls.append((user_id, deactivated))
        if user_id in self.fail_ids:
            raise ad_sync.SyncError("HTTP 500")
        for u in self._users:
            if u.id == user_id:
                u.deactivated = deactivated


class RunTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.state = os.path.join(self.tmp.name, "state.json")
        self.c = cfg(state_file=self.state)

    def tearDown(self):
        self.tmp.cleanup()

    def test_admin_reactivation_clears_state_and_manual_deactivation_stays(self):
        u = user("u1", "dorj")
        chatto = _RecordingChatto([u])
        # 1. dorj leaves: the sync deactivates and records the account.
        ad_sync.run(self.c, True, False, [ad("dorj", False), ad("x")], chatto, io.StringIO())
        self.assertTrue(u.deactivated)
        self.assertEqual(ad_sync.load_state(self.state), {"u1"})
        # 2. An admin reactivates dorj by hand while AD still says "left".
        #    The sync deactivates again (AD wins) and keeps the record.
        u.deactivated = False
        ad_sync.run(self.c, True, False, [ad("dorj", False), ad("x")], chatto, io.StringIO())
        self.assertTrue(u.deactivated)
        # 3. dorj comes back in AD, and the admin reactivates first.
        u.deactivated = False
        ad_sync.run(self.c, True, False, [ad("dorj"), ad("x")], chatto, io.StringIO())
        self.assertEqual(ad_sync.load_state(self.state), set())
        # 4. Later an admin deactivates dorj by hand. The sync must not undo it.
        u.deactivated = True
        chatto.calls.clear()
        ad_sync.run(self.c, True, False, [ad("dorj"), ad("x")], chatto, io.StringIO())
        self.assertTrue(u.deactivated)
        self.assertEqual(chatto.calls, [])

    def test_dry_run_writes_no_state(self):
        ad_sync.save_state(self.state, {"u1"})
        chatto = _RecordingChatto([user("u1", "dorj")])  # active again
        ad_sync.run(self.c, False, False, [ad("dorj")], chatto, io.StringIO())
        self.assertEqual(ad_sync.load_state(self.state), {"u1"})
        self.assertEqual(chatto.calls, [])

    def test_failures_are_counted_and_not_recorded(self):
        chatto = _RecordingChatto([user("u1", "bat"), user("u2", "dorj")], fail_ids={"u1"})
        out = io.StringIO()
        with unittest.mock.patch("sys.stderr", io.StringIO()) as err:
            code = ad_sync.run(self.c, True, False, [ad("bat", False), ad("dorj", False), ad("x")], chatto, out)
        self.assertEqual(code, 1)
        self.assertIn("1 failure(s)", out.getvalue())
        self.assertIn("error bat", err.getvalue())
        self.assertEqual(ad_sync.load_state(self.state), {"u2"})

    def test_reactivation_is_planned_only_for_our_accounts(self):
        ad_sync.save_state(self.state, {"u1"})
        chatto = _RecordingChatto([user("u1", "bat", deactivated=True), user("u2", "dorj", deactivated=True)])
        ad_sync.run(self.c, True, False, [ad("bat"), ad("dorj")], chatto, io.StringIO())
        self.assertEqual(chatto.calls, [("u1", False)])
        self.assertEqual(ad_sync.load_state(self.state), set())


class ChattoClientTest(unittest.TestCase):
    def setUp(self):
        FakeChatto.calls = []
        FakeChatto.members = {
            "u1": member("u1", "Bat", verifiedEmails=["Bat@Corp.mn"]),
            "u2": member("u2", "gone", deleted=True),
            "u3": member("u3", "helper", bot={"ownerUserId": "u1"}),
            "u4": member("u4", "c"),
            "u5": member("u5", "d", deactivated=True),
        }
        self.srv = HTTPServer(("127.0.0.1", 0), FakeChatto)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.url = f"http://127.0.0.1:{self.srv.server_port}"

    def tearDown(self):
        self.srv.shutdown()
        self.srv.server_close()

    def test_users_pages_and_skips_bots_and_deleted(self):
        users = ad_sync.Chatto(self.url, "t").users()
        self.assertEqual([u.id for u in users], ["u1", "u4", "u5"])
        self.assertEqual(users[0].login, "bat")
        self.assertEqual(users[0].emails, {"bat@corp.mn"})
        self.assertTrue(users[2].deactivated)
        # The fake returns 2 IDs per page: 3 ListMembers calls with offsets 0, 2, 4.
        offsets = [b["page"]["offset"] for p, _, b in FakeChatto.calls if p.endswith("ListMembers")]
        self.assertEqual(offsets, [0, 2, 4])

    def test_http_error_becomes_sync_error(self):
        with self.assertRaises(ad_sync.SyncError) as ctx:
            ad_sync.Chatto(self.url, "t").call("chatto.admin.v1.AdminUserService", "Unknown", {})
        self.assertIn("HTTP 404", str(ctx.exception))

    def test_unreachable_server_becomes_sync_error(self):
        self.srv.shutdown()
        self.srv.server_close()
        with self.assertRaises(ad_sync.SyncError) as ctx:
            ad_sync.Chatto(self.url, "t").users()
        self.assertIn("cannot reach Chatto", str(ctx.exception))
        # tearDown shuts down a running server.
        self.srv = HTTPServer(("127.0.0.1", 0), FakeChatto)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()


class HelperTest(unittest.TestCase):
    def test_escape(self):
        self.assertEqual(ad_sync._escape("CN=A (B)*\\x"), "CN=A \\28B\\29\\2a\\5cx")

    def test_upn_login_drops_the_domain(self):
        c = cfg(login_attr="userPrincipalName")
        a = ad_sync.classify("cn=bat", {"userPrincipalName": ["Bat@corp.local"], "userAccountControl": [512]}, c, None, NOW)
        self.assertEqual(a.login, "bat")
        self.assertIn("bat@corp.local", a.emails)

    def test_account_expires_decoded_by_ldap3(self):
        # ldap3 can return accountExpires as datetime values.
        never = dt.datetime(9999, 12, 31, 23, 59, 59, tzinfo=dt.timezone.utc)
        self.assertEqual(ad_sync._as_int(never), 0)
        self.assertEqual(ad_sync._as_int(ad_sync.FILETIME_EPOCH), 0)
        c = cfg()
        past = NOW - dt.timedelta(days=1)
        a = ad_sync.classify("cn=x", {"sAMAccountName": ["x"], "accountExpires": [past]}, c, None, NOW)
        self.assertEqual(a.reason, "expired in AD")

    def test_attribute_names_are_case_insensitive(self):
        a = ad_sync.classify("cn=x", {"SAMACCOUNTNAME": ["X"], "useraccountcontrol": [514]}, cfg(), None, NOW)
        self.assertEqual((a.login, a.reason), ("x", "disabled in AD"))


class SecretFileTest(unittest.TestCase):
    def test_password_and_token_files(self):
        with tempfile.TemporaryDirectory() as d:
            pw, tok = os.path.join(d, "pw"), os.path.join(d, "tok")
            with open(pw, "w") as f:
                f.write("secret\n")
            with open(tok, "w") as f:
                f.write("cht_BK_abc\n")
            c = ad_sync.config_from_env({"AD_LDAP_URL": "ldaps://dc1", "AD_BIND_DN": "cn=svc",
                                         "AD_BIND_PASSWORD_FILE": pw, "CHATTO_URL": "https://chat",
                                         "CHATTO_TOKEN_FILE": tok})
            self.assertEqual((c.bind_password, c.chatto_token), ("secret", "cht_BK_abc"))
            self.assertEqual(c.base_dn, "")

    def test_missing_secret_file_is_a_sync_error(self):
        env = {"AD_LDAP_URL": "ldaps://dc1", "AD_BIND_DN": "cn=svc", "CHATTO_URL": "https://chat",
               "CHATTO_TOKEN": "t", "AD_BIND_PASSWORD_FILE": "/nonexistent/ad-password"}
        with self.assertRaises(ad_sync.SyncError) as ctx:
            ad_sync.config_from_env(env)
        self.assertIn("AD_BIND_PASSWORD_FILE", str(ctx.exception))

    def test_main_reports_config_errors_with_exit_code_2(self):
        with unittest.mock.patch.dict(os.environ, {}, clear=True), \
             unittest.mock.patch("sys.stderr", io.StringIO()) as err:
            self.assertEqual(ad_sync.main([]), 2)
        self.assertIn("ad-sync:", err.getvalue())


if __name__ == "__main__":
    unittest.main()
