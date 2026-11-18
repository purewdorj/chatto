#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Setunari Group LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Setunari: keep only current employees active in Chatto.

Reads user accounts from Active Directory over LDAP and compares them with the
human accounts of a Chatto server. A Chatto account whose AD account is no
longer a current employee is deactivated (data and messages stay, sign-in and
sessions stop). An account that this script deactivated is reactivated when
the person is a current employee again. Accounts that the script did not
deactivate are never reactivated, so manual deactivations by admins stay.

Current employee = AD user account that
  * is enabled (userAccountControl bit ACCOUNTDISABLE not set),
  * has not expired (accountExpires),
  * is under AD_BASE_DN and not under any AD_EXCLUDE_OUS,
  * is a (nested) member of AD_EMPLOYEE_GROUP_DN, when that is set.

Chatto access uses a bot API key (Authorization: Bearer cht_BK_...). The bot
needs the server permissions admin.view-users and user.manage-accounts.

Default is a dry run that prints the plan. Pass --apply to change Chatto.
Configuration comes from environment variables (see README.md).
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import sys
import urllib.error
import urllib.request
from dataclasses import dataclass, field

UAC_ACCOUNTDISABLE = 0x2
# AD stores "never expires" as 0 or as the maximum int64.
NEVER_EXPIRES = {0, 0x7FFFFFFFFFFFFFFF}
FILETIME_EPOCH = dt.datetime(1601, 1, 1, tzinfo=dt.timezone.utc)
# LDAP_MATCHING_RULE_IN_CHAIN: transitive (nested) group membership.
IN_CHAIN = "1.2.840.113556.1.4.1941"
USER_FILTER = "(&(objectCategory=person)(objectClass=user))"


class SyncError(Exception):
    pass


# ---------------------------------------------------------------- configuration


@dataclass
class Config:
    ldap_url: str
    bind_dn: str
    bind_password: str
    base_dn: str
    employee_group_dn: str = ""
    exclude_ous: list[str] = field(default_factory=list)
    login_attr: str = "sAMAccountName"
    email_attrs: list[str] = field(default_factory=lambda: ["mail", "userPrincipalName"])
    ldap_ca_file: str = ""
    chatto_url: str = ""
    chatto_token: str = ""
    keep_logins: set[str] = field(default_factory=set)
    unmatched: str = "ignore"
    max_deactivations: int = 25
    state_file: str = "ad-sync-state.json"


def _split(value: str) -> list[str]:
    return [p.strip() for p in value.replace("\n", ";").split(";") if p.strip()]


def config_from_env(env: dict[str, str]) -> Config:
    def need(name: str) -> str:
        value = env.get(name, "").strip()
        if not value:
            raise SyncError(f"{name} is not set")
        return value

    password = env.get("AD_BIND_PASSWORD", "")
    password_file = env.get("AD_BIND_PASSWORD_FILE", "").strip()
    if password_file:
        with open(password_file, encoding="utf-8") as f:
            password = f.read().strip()
    if not password:
        raise SyncError("AD_BIND_PASSWORD or AD_BIND_PASSWORD_FILE is not set")

    token = env.get("CHATTO_TOKEN", "")
    token_file = env.get("CHATTO_TOKEN_FILE", "").strip()
    if token_file:
        with open(token_file, encoding="utf-8") as f:
            token = f.read().strip()
    if not token:
        raise SyncError("CHATTO_TOKEN or CHATTO_TOKEN_FILE is not set")

    unmatched = env.get("ADSYNC_UNMATCHED", "ignore").strip().lower() or "ignore"
    if unmatched not in ("ignore", "deactivate"):
        raise SyncError("ADSYNC_UNMATCHED must be 'ignore' or 'deactivate'")

    return Config(
        ldap_url=need("AD_LDAP_URL"),
        bind_dn=need("AD_BIND_DN"),
        bind_password=password,
        base_dn=need("AD_BASE_DN"),
        employee_group_dn=env.get("AD_EMPLOYEE_GROUP_DN", "").strip(),
        exclude_ous=_split(env.get("AD_EXCLUDE_OUS", "")),
        login_attr=env.get("AD_LOGIN_ATTR", "sAMAccountName").strip() or "sAMAccountName",
        email_attrs=[a.strip() for a in env.get("AD_EMAIL_ATTRS", "mail,userPrincipalName").split(",") if a.strip()],
        ldap_ca_file=env.get("AD_CA_FILE", "").strip(),
        chatto_url=need("CHATTO_URL").rstrip("/"),
        chatto_token=token,
        keep_logins={l.strip().lower() for l in env.get("ADSYNC_KEEP_LOGINS", "").split(",") if l.strip()},
        unmatched=unmatched,
        max_deactivations=int(env.get("ADSYNC_MAX_DEACTIVATIONS", "25")),
        state_file=env.get("ADSYNC_STATE_FILE", "ad-sync-state.json").strip() or "ad-sync-state.json",
    )


# ------------------------------------------------------------- Active Directory


@dataclass
class ADAccount:
    dn: str
    login: str
    emails: set[str]
    current: bool
    reason: str  # why the account is not current; empty when current


def _first(entry_attrs: dict, name: str):
    for key, value in entry_attrs.items():
        if key.lower() == name.lower():
            if isinstance(value, (list, tuple)):
                return value[0] if value else None
            return value
    return None


def _as_int(value) -> int:
    if value is None or value == "":
        return 0
    if isinstance(value, bytes):
        value = value.decode()
    if isinstance(value, dt.datetime):
        # ldap3 may already decode accountExpires; treat far-future as never.
        if value.year >= 9999 or value <= FILETIME_EPOCH:
            return 0
        return int((value - FILETIME_EPOCH).total_seconds() * 10_000_000)
    return int(value)


def _norm_dn(dn: str) -> str:
    return ",".join(part.strip() for part in dn.lower().split(","))


def classify(dn: str, attrs: dict, cfg: Config, group_members: set[str] | None, now: dt.datetime) -> ADAccount:
    login = str(_first(attrs, cfg.login_attr) or "").strip()
    if cfg.login_attr.lower() == "userprincipalname" and "@" in login:
        login = login.split("@", 1)[0]
    emails = set()
    for attr in cfg.email_attrs:
        value = _first(attrs, attr)
        if value:
            emails.add(str(value).strip().lower())

    reason = ""
    uac = _as_int(_first(attrs, "userAccountControl"))
    expires = _as_int(_first(attrs, "accountExpires"))
    ndn = _norm_dn(dn)
    if uac & UAC_ACCOUNTDISABLE:
        reason = "disabled in AD"
    elif expires not in NEVER_EXPIRES and FILETIME_EPOCH + dt.timedelta(microseconds=expires / 10) <= now:
        reason = "expired in AD"
    elif any(ndn.endswith("," + _norm_dn(ou)) for ou in cfg.exclude_ous):
        reason = "in excluded OU"
    elif group_members is not None and ndn not in group_members:
        reason = "not in employee group"
    return ADAccount(dn=dn, login=login.lower(), emails=emails, current=not reason, reason=reason)


def read_ad(cfg: Config, connection=None, now: dt.datetime | None = None) -> list[ADAccount]:
    import ldap3  # imported here so tests and --help work without a server

    now = now or dt.datetime.now(dt.timezone.utc)
    conn = connection
    if conn is None:
        tls = None
        if cfg.ldap_ca_file:
            import ssl

            tls = ldap3.Tls(validate=ssl.CERT_REQUIRED, ca_certs_file=cfg.ldap_ca_file)
        server = ldap3.Server(cfg.ldap_url, tls=tls, get_info=ldap3.NONE, connect_timeout=15)
        conn = ldap3.Connection(
            server, user=cfg.bind_dn, password=cfg.bind_password, auto_bind=True,
            raise_exceptions=True, receive_timeout=60,
        )

    attributes = sorted({cfg.login_attr, "userAccountControl", "accountExpires", *cfg.email_attrs})

    def search(search_filter: str, attrs: list[str]) -> list[tuple[str, dict]]:
        out = []
        for entry in conn.extend.standard.paged_search(
            cfg.base_dn, search_filter, attributes=attrs, paged_size=500, generator=True,
        ):
            if entry.get("type") == "searchResEntry":
                out.append((entry["dn"], entry.get("attributes", {})))
        return out

    group_members = None
    if cfg.employee_group_dn:
        group_filter = f"(&{USER_FILTER}(memberOf:{IN_CHAIN}:={_escape(cfg.employee_group_dn)}))"
        group_members = {_norm_dn(dn) for dn, _ in search(group_filter, ["distinguishedName"])}

    accounts = [classify(dn, attrs, cfg, group_members, now) for dn, attrs in search(USER_FILTER, attributes)]
    return [a for a in accounts if a.login or a.emails]


def _escape(value: str) -> str:
    out = []
    for ch in value:
        if ch in "\\*()\x00":
            out.append("\\%02x" % ord(ch))
        else:
            out.append(ch)
    return "".join(out)


# ----------------------------------------------------------------------- Chatto


@dataclass
class ChattoUser:
    id: str
    login: str
    emails: set[str]
    deactivated: bool
    can_change: bool


class Chatto:
    def __init__(self, base_url: str, token: str, opener=None):
        self.base = base_url.rstrip("/") + "/api/connect"
        self.token = token
        self.opener = opener or urllib.request.urlopen

    def call(self, service: str, method: str, body: dict) -> dict:
        req = urllib.request.Request(
            f"{self.base}/{service}/{method}",
            data=json.dumps(body).encode(),
            method="POST",
            headers={
                "Content-Type": "application/json",
                "Connect-Protocol-Version": "1",
                "Authorization": f"Bearer {self.token}",
            },
        )
        try:
            with self.opener(req, timeout=60) as resp:
                return json.loads(resp.read() or b"{}")
        except urllib.error.HTTPError as err:
            detail = err.read().decode(errors="replace")[:300]
            raise SyncError(f"Chatto {method}: HTTP {err.code}: {detail}") from None
        except (urllib.error.URLError, OSError) as err:
            raise SyncError(f"cannot reach Chatto at {self.base}: {err}") from None

    def users(self) -> list[ChattoUser]:
        svc = "chatto.admin.v1.AdminUserService"
        ids: list[str] = []
        offset = 0
        while True:
            page = self.call(svc, "ListMembers", {"page": {"limit": 100, "offset": offset}})
            batch = page.get("userIds", [])
            ids.extend(batch)
            offset += len(batch)
            if not page.get("page", {}).get("hasMore") or not batch:
                break
        users = []
        for i in range(0, len(ids), 100):
            resp = self.call(svc, "BatchGetMembers", {"userIds": ids[i:i + 100]})
            for m in resp.get("members", []):
                u = m.get("user", {})
                if u.get("deleted") or u.get("bot"):
                    continue
                users.append(ChattoUser(
                    id=u["id"],
                    login=u.get("login", "").lower(),
                    emails={e.lower() for e in m.get("verifiedEmails", [])},
                    deactivated=bool(m.get("deactivated") or u.get("deactivated")),
                    can_change=m.get("viewerCanChangeDeactivation", True),
                ))
        return users

    def set_deactivated(self, user_id: str, deactivated: bool) -> None:
        self.call("chatto.admin.v1.AdminUserService", "SetUserDeactivated",
                  {"userId": user_id, "deactivated": deactivated})


# ------------------------------------------------------------------------- plan


@dataclass
class Action:
    user: ChattoUser
    deactivate: bool
    reason: str


def plan(cfg: Config, ad: list[ADAccount], users: list[ChattoUser], ours: set[str]) -> tuple[list[Action], list[str]]:
    by_login: dict[str, list[ADAccount]] = {}
    by_email: dict[str, list[ADAccount]] = {}
    for a in ad:
        if a.login:
            by_login.setdefault(a.login, []).append(a)
        for e in a.emails:
            by_email.setdefault(e, []).append(a)

    actions: list[Action] = []
    notes: list[str] = []
    for u in sorted(users, key=lambda x: x.login):
        if u.login in cfg.keep_logins:
            continue
        matches = list(by_login.get(u.login, []))
        if not matches:
            for e in u.emails:
                matches.extend(by_email.get(e, []))
        if matches:
            current = [a for a in matches if a.current]
            if current:
                if u.deactivated and u.id in ours:
                    actions.append(Action(u, False, "current employee again"))
                continue
            if not u.deactivated:
                actions.append(Action(u, True, matches[0].reason))
            continue
        # No AD account for this Chatto user (local admin, test account, ...).
        if cfg.unmatched == "deactivate" and not u.deactivated:
            actions.append(Action(u, True, "no AD account"))
        elif not u.deactivated:
            notes.append(f"{u.login}: no AD account, left unchanged")
    return actions, notes


def load_state(path: str) -> set[str]:
    try:
        with open(path, encoding="utf-8") as f:
            return set(json.load(f).get("deactivated_by_sync", []))
    except FileNotFoundError:
        return set()


def save_state(path: str, ids: set[str]) -> None:
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump({"deactivated_by_sync": sorted(ids)}, f, indent=2)
        f.write("\n")
    os.replace(tmp, path)


def run(cfg: Config, apply: bool, force: bool, ad_accounts: list[ADAccount], chatto: Chatto, out=sys.stdout) -> int:
    current = sum(1 for a in ad_accounts if a.current)
    if current == 0:
        raise SyncError("AD returned no current employees; check AD_BASE_DN and AD_EMPLOYEE_GROUP_DN. Nothing changed.")
    users = chatto.users()
    ours = load_state(cfg.state_file)
    actions, notes = plan(cfg, ad_accounts, users, ours)

    print(f"AD: {len(ad_accounts)} accounts, {current} current employees. Chatto: {len(users)} human accounts.", file=out)
    for n in notes:
        print(f"  note  {n}", file=out)
    deactivations = [a for a in actions if a.deactivate]
    for a in actions:
        verb = "deactivate" if a.deactivate else "reactivate"
        skip = "" if a.user.can_change else "  (skipped: owner or not allowed)"
        print(f"  {verb:<10} {a.user.login}  ({a.reason}){skip}", file=out)
    if not actions:
        print("  nothing to change", file=out)

    if len(deactivations) > cfg.max_deactivations and not force:
        raise SyncError(
            f"{len(deactivations)} deactivations exceed ADSYNC_MAX_DEACTIVATIONS={cfg.max_deactivations}; "
            "check the plan and run again with --force. Nothing changed.")
    if not apply:
        print("Dry run. Run with --apply to make these changes.", file=out)
        return 0

    failures = 0
    for a in actions:
        if not a.user.can_change:
            continue
        try:
            chatto.set_deactivated(a.user.id, a.deactivate)
        except SyncError as err:
            failures += 1
            print(f"  error {a.user.login}: {err}", file=sys.stderr)
            continue
        if a.deactivate:
            ours.add(a.user.id)
        else:
            ours.discard(a.user.id)
        save_state(cfg.state_file, ours)
    print(f"Applied {len(actions) - failures} change(s), {failures} failure(s).", file=out)
    return 1 if failures else 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Deactivate Chatto accounts of people who are no longer current employees in AD.")
    parser.add_argument("--apply", action="store_true", help="make the changes (default: dry run)")
    parser.add_argument("--force", action="store_true", help="allow more deactivations than ADSYNC_MAX_DEACTIVATIONS")
    args = parser.parse_args(argv)
    try:
        cfg = config_from_env(dict(os.environ))
        try:
            accounts = read_ad(cfg)
        except SyncError:
            raise
        except Exception as err:  # ldap3 and socket errors: one readable line
            raise SyncError(f"cannot read AD at {cfg.ldap_url}: {type(err).__name__}: {err}") from None
        return run(cfg, args.apply, args.force, accounts, Chatto(cfg.chatto_url, cfg.chatto_token))
    except SyncError as err:
        print(f"ad-sync: {err}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
