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


if __name__ == "__main__":
    unittest.main()
