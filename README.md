# Telegram Time Tracker

A Telegram bot that tracks working hours and turns them into money. Add a
project, tell it what that project pays — by the hour or by the month — then
tap **Start work** when you begin and **Stop work** when you finish. The bot
reports what each shift paid and keeps running totals for the day, the week and
the month, broken down per project.

Written in Go with a single direct dependency, storing everything in one
SQLite file.

---

## Features

- **Multiple projects** — bill one client by the hour and draw a salary from
  another. Each project carries its own rate, and starting a shift asks which
  one you are working on.
- **Hourly or monthly pay** — set an hourly rate, or a monthly salary and the
  hourly rate is worked out for you. Both amounts are remembered per project,
  so switching between them is one tap.
- **Start / stop buttons** — one running shift at a time whatever the project,
  enforced by the database rather than by hope.
- **Per shift payout** — ending a shift shows its length, the rate applied and
  what it earned.
- **Per day, per week, per month totals** — hours, shift count and earnings for
  each period, plus a per project breakdown of the month.
- **Timezone aware** — your day starts and ends where you live, not in UTC.
- **Any currency** — a plain label, from `USD` to `€` to `zł`.
- **Exact money** — amounts are integer cents end to end, so totals never
  drift, and a full month on a salary pays exactly that salary.
- **A real reset** — `/reset` clears every project, rate and shift, after
  showing you exactly what it is about to destroy.

---

## Quick start

**1. Create a bot.** Message [@BotFather](https://t.me/BotFather) on Telegram,
send `/newbot`, and copy the token it gives you.

**2. Configure.**

```bash
cp .env.example .env
```

Put your token in `.env`:

```
TELEGRAM_BOT_TOKEN=123456789:AAE...
```

**3. Run.**

```bash
make run
```

The database and its directory are created on first start. Open your bot in
Telegram, send `/start`, then add your first project:

```
/newproject Acme
```

Set what it pays, and you are ready to clock in.

---

## Using the bot

The keyboard under the message box carries everything you need day to day:

| Button | What it does |
| --- | --- |
| ▶️ Start work | Clocks you in |
| ⏹ Stop work | Clocks you out and shows the payout |
| 📊 Today | Hours and earnings for today |
| 📈 Statistics | Day, week and month totals, per project |
| 📁 Projects | Add, rename, delete, and set rates |
| ⚙️ Settings | Currency, timezone and reset |
| ❓ Help | How it all works |

The same actions are available as commands:

```
/work                     Start a shift
/stop                     Stop the running shift
/today                    Today's hours and pay
/stats                    Day, week and month totals
/projects                 Your projects and their rates
/newproject Acme          Add a project
/settings                 Show settings with edit buttons
/currency EUR             Set the currency label
/timezone Europe/Kyiv     Set the timezone
/reset                    Delete every project and all history
```

Rates are set from a project's card, reached through 📁 Projects — each project
has its own, so there is no account wide rate to collide with.

A typical exchange, once more than one project exists:

```
You  ▶️ Start work
Bot  ▶️ Which project?
     [ Acme ]  [ Day job ]

You  (taps Acme)
Bot  ▶️ Shift started — Acme
     🕒 Since 09:00
     💵 Rate: 25.50 USD/h

… four hours later …

You  ⏹ Stop work
Bot  ⏹ Shift finished — Acme
     🕒 09:00 → 13:00
     ⏱ Worked: 4h 00m (4.00 h)
     💵 Rate: 25.50 USD/h
     💰 This shift: 102.00 USD

     📅 Today, Wed 19 Aug (all projects)
        4.00 h across 1 shift
        Earned: 102.00 USD
```

With a single project there is nothing to choose, so ▶️ Start work begins it
straight away.

On a salary the receipt shows the rate both ways, so you can see what an hour
of your month is actually worth:

```
You  (taps 🗓️ Monthly rate on the Day job card, sends 3360)
Bot  ✅ Day job now pays 3 360.00 USD, which is 19.38 USD/h
     over 173.33 hours a month.

You  ⏹ Stop work
Bot  ⏹ Shift finished — Day job
     ⏱ Worked: 8h 00m (8.00 h)
     💵 Rate: 3 360.00 USD/month (≈ 19.38 USD/h)
     💰 This shift: 155.08 USD
```

---

## Configuration

All settings come from the environment. A `.env` file in the working directory
is loaded at startup; real environment variables win over it.

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | yes | — | Token from @BotFather |
| `DATABASE_PATH` | no | `data/timetracker.db` | SQLite file, created if missing |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` |

---

## How it works

A few decisions are worth knowing about, because they decide what the numbers
mean.

**A monthly salary is converted at 173⅓ hours a month** — 52 weeks × 40 hours
÷ 12 — so a salary of 3360 works out to about 19.38 an hour. Pay is computed
straight from the monthly figure rather than from that rounded hourly rate:

```
earnings = monthly_cents × seconds ÷ 624000
```

Rounding to whole cents per hour first would lose about 80 cents over a month.
Going direct means working exactly 173⅓ hours pays exactly your salary, which
is the number a salaried user will check first.

**Rates belong to projects, not to you.** Two projects can be paid completely
differently, and each shift is paid whatever its own project was carrying when
it closed.

**Only one shift runs at a time**, whichever project it is on. A partial unique
index over open shifts means SQLite rejects a second one, so a double tap or
two devices racing cannot produce overlapping work.

**A shift belongs to the day it started.** Clock in at 22:00 and out at 02:00
and the whole five hours count on the first day. This keeps night shifts in one
piece instead of splitting them across two reports.

**Weeks run Monday to Sunday**, and days and months are bounded by your
timezone, not the server's. Set `/timezone` before you rely on the totals.

**The rate is snapshotted when a shift ends.** Raising a project's rate — or
switching it between hourly and monthly pay — changes what you earn from then
on; finished shifts keep the rate and mode they were paid under, so history
never rewrites itself.

**Deleting is deliberate.** Removing a project deletes the shifts recorded
against it, and `/reset` deletes everything you have. Both ask first and show
the damage — how many shifts, how many hours — before anything goes.

**Money is integer cents everywhere.** Earnings are computed as
`round(rate_cents × seconds ÷ 3600)` with integer arithmetic, so a hundred
one-hour shifts sum to exactly the same total as one hundred-hour span.

**Running shifts are excluded from totals.** They are reported separately, so
the day total only ever moves when a shift actually closes.

---

## Project layout

```
main.go                Wiring, config, graceful shutdown
migrations/*.sql       Numbered schema steps, embedded into the binary
internal/
  bot/                 Update loop, handlers, keyboards, message text
  worklog/             Domain: projects, rates, shifts, totals, periods
  storage/             SQLite repository and the migration runner
  telegram/            Bot API client (standard library only)
  money/               Cent parsing, rounding and formatting
  config/              Environment and .env loading
```

The only direct dependency is [`modernc.org/sqlite`](https://modernc.org/sqlite),
a pure Go SQLite driver — no cgo, so the binary is static and cross-compiles
cleanly. The Telegram client is written against `net/http` directly.

---

## Database

```sql
users   id, chat_id, username, first_name, hourly_rate_cents,
        currency, timezone, pending_action, created_at, updated_at

shifts  id, user_id, started_at, ended_at, work_date, timezone,
        duration_seconds, rate_cents, earnings_cents, currency, created_at
```

Instants are RFC 3339 UTC text, so the file stays readable in the `sqlite3`
shell. `work_date` is the local calendar date the shift began on and is what
the reports group by.

Migrations live in `migrations/`, are embedded in the binary and run on every
start. Each file is applied once inside a transaction and recorded in
`schema_migrations`, so an existing database upgrades in place rather than
needing a reset. To change the schema, add a new numbered file — never edit one
that has already shipped.

```bash
make db          # open the database in the sqlite3 shell
make reset-db    # delete the database and start over
```

---

## Development

```bash
make             # list all targets
make test        # run the test suite
make test-race   # run it under the race detector
make lint        # gofmt check plus go vet
make build       # compile to bin/timetracker
make cover       # coverage report
```

Tests cover the money arithmetic in both pay modes and the day/week/month
boundary maths as unit tests, and everything above that runs against a real
temporary SQLite database — the service rules, project isolation between users,
concurrent start and stop attempts, in-place upgrades of older databases, and
the message handlers end to end against a fake Bot API server. Running the
handlers on real storage rather than a hand written fake keeps the two from
drifting apart. No network or running database is needed.

---

## Deployment

**Docker.** The image is `distroless/static` with the tz database compiled into
the binary.

```bash
make docker
docker run -d --name timetracker \
  --restart unless-stopped \
  -e TELEGRAM_BOT_TOKEN=your-token \
  -v timetracker-data:/app/data \
  timetracker:latest
```

**systemd.** Build with `make build`, then:

```ini
[Unit]
Description=Telegram time tracker
After=network-online.target

[Service]
WorkingDirectory=/opt/timetracker
ExecStart=/opt/timetracker/bin/timetracker
EnvironmentFile=/opt/timetracker/.env
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Only one instance may poll a given bot token at a time — a second one gets
`409 Conflict` from Telegram and neither works properly. Stop the old process
before starting a new one.

Back up `data/timetracker.db` together with its `-wal` sidecar, or run
`sqlite3 data/timetracker.db ".backup backup.db"` for a consistent snapshot.

---

## Limits

- Long polling, not webhooks. Fine for personal and small team use; a webhook
  would suit high traffic better.
- SQLite serialises writes. That is ample here — a shift is two writes a day
  per person — but it is not the choice for thousands of concurrent writers.
- Shifts are not editable after the fact. There is no `/edit` or manual entry
  yet; fixing a mistake means going into the database.
- Projects cannot be archived, only deleted, which takes their history with
  them. If you want to stop using one but keep its numbers, leave it in place.
- `/reset` is per account. There is no admin command to wipe the whole
  database; delete the file for that.
- The monthly conversion norm is fixed at 173⅓ hours. If your contract counts a
  month differently, that constant lives in `internal/money/money.go`.
