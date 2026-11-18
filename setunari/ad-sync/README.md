# AD sync: only current employees in Setunari Chat

`ad_sync.py` compares Active Directory with the Chatto server and deactivates the
Chatto accounts of people who no longer work at Setunari. Deactivation is Chatto's
own account deactivation ([FDR-018](../../docs/fdr/FDR-018-account-lifecycle.md)):
sign-in and all sessions stop at once, messages and the account stay, and the
account can be reactivated. Nothing is deleted.

Sign-in itself still goes through the OIDC provider that is connected to AD. This
script handles what the provider cannot: people who left keep their open sessions
and stay in the people directory until their account is deactivated.

## Rules

A **current employee** is an AD user under `AD_BASE_DN` (empty: the whole domain) that

- is enabled (`userAccountControl` without `ACCOUNTDISABLE`),
- has not expired (`accountExpires`),
- is not under one of `AD_EXCLUDE_OUS`,
- is a member of `AD_EMPLOYEE_GROUP_DN`, also through nested groups (only when set).

Each human Chatto account is matched to AD by verified email first (`AD_EMAIL_ATTRS`,
default `mail,userPrincipalName`). If no verified email matches, the login is used
(`AD_LOGIN_ATTR`, default `sAMAccountName`, case-insensitive). Users can change their
Chatto login, so a login match is weaker than an email match. When the login and the
email match different AD accounts, the email wins and the output shows a note.

An account that is not under `AD_BASE_DN` is "no AD account", not "former employee".
If people who leave are moved to an OU outside `AD_BASE_DN`, or their AD account is
deleted, the script does not deactivate them (with the default `ADSYNC_UNMATCHED=ignore`).
Keep `AD_BASE_DN` empty (the whole domain) and use `AD_EXCLUDE_OUS` for leaver OUs.

| Chatto account                                     | Action                                           |
| -------------------------------------------------- | ------------------------------------------------ |
| AD account is a current employee                   | none                                             |
| AD account is not a current employee               | **deactivate**                                   |
| deactivated by this script, current employee again | **reactivate**                                   |
| deactivated by an admin                            | none (never reactivated by the script)           |
| no AD account (local admin, test account)          | none; `ADSYNC_UNMATCHED=deactivate` changes this |
| login in `ADSYNC_KEEP_LOGINS`, bots, server owners | none                                             |

The state file (`ADSYNC_STATE_FILE`) lists the accounts the script deactivated.
Keep it between runs. When an account in the list is active again (an admin or the
script reactivated it), the script removes it from the list. A later manual
deactivation then stays.

Safety: the script changes nothing when AD returns no current employees (wrong base
DN or group), or when more than `ADSYNC_MAX_DEACTIVATIONS` (default 25) accounts
would be deactivated. Check the plan and run with `--force` in that case.

## Setup

1. **AD service account.** A normal user with read access is enough. Use LDAPS
   (`ldaps://…:636`); set `AD_CA_FILE` if the domain CA is not in the system trust store.
2. **Chatto bot.** As an admin: Server Admin → Bots → create a bot (for example
   `ad-sync`). Under **Permissions** allow, at server scope, `admin.view-users`
   and `user.manage-accounts`. Under **API keys** create a key and save it to the
   file in `CHATTO_TOKEN_FILE`. The bot's owner must have these permissions too
   (the `admin` role has them). A bot key does not need privileged mode.
3. **Configuration.** Copy `ad-sync.env.example` to `ad-sync.env`, fill it in,
   `chmod 600`. The shell loads this file, so put a value that contains spaces in
   double quotes, for example `AD_EMPLOYEE_GROUP_DN="CN=Chat Users,OU=Groups,DC=example,DC=local"`.
   Without quotes, the shell cuts the value at the first space.
4. **Install.** On the host that runs it (needs network access to a domain
   controller and to the Chatto URL):

   ```bash
   python3 -m venv /opt/setunari-ad-sync
   /opt/setunari-ad-sync/bin/pip install -r setunari/ad-sync/requirements.txt
   cp setunari/ad-sync/ad_sync.py /opt/setunari-ad-sync/
   ```

## Run

```bash
set -a; . /etc/setunari/ad-sync/ad-sync.env; set +a
/opt/setunari-ad-sync/bin/python /opt/setunari-ad-sync/ad_sync.py           # dry run: prints the plan
/opt/setunari-ad-sync/bin/python /opt/setunari-ad-sync/ad_sync.py --apply   # makes the changes
```

Example output:

```
AD: 412 accounts, 268 current employees. Chatto: 231 human accounts.
  note  admin: no AD account, left unchanged
  deactivate bat.e  (disabled in AD)
  deactivate saraa  (not in employee group)
  reactivate tuul   (current employee again)
Dry run. Run with --apply to make these changes.
```

Run it every 15 minutes with cron after the first dry run looks right:

```cron
*/15 * * * * root set -a; . /etc/setunari/ad-sync/ad-sync.env; set +a; /opt/setunari-ad-sync/bin/python /opt/setunari-ad-sync/ad_sync.py --apply >> /var/log/setunari-ad-sync.log 2>&1
```

Exit codes: `0` success, `1` some changes failed, `2` configuration, AD, or Chatto error
or a safety stop.

## Also restrict sign-in at the provider

To stop new sign-ins right away (before the next sync run), let the OIDC provider
send an AD groups claim and set `allowed_groups` on the Chatto provider, for example
`CHATTO_AUTH_PROVIDERS_0_ALLOWED_GROUPS="Chat Users"` ([FDR-023](../../docs/fdr/FDR-023-authentication-and-sessions.md)).
Existing sessions are not ended by that setting; the sync ends them.

## Tests

```bash
pip install ldap3
python3 -m unittest setunari/ad-sync/test_ad_sync.py
```

The tests use a fake Chatto API and ldap3's mock directory. The mock directory does
not support nested-group search, so test `AD_EMPLOYEE_GROUP_DN` with a dry run
against the real AD.
