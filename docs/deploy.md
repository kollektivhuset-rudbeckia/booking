# Utrullning till quebec

Bokningen körs som en container på quebec och håller allt i en enda
SQLite-fil. Efter den första handpåläggningen sköter sig servern själv: en
push till `main` bygger en image, och när bygget lyckats hämtar servern den
och startar om.

## Vad som är tillstånd, och vad som inte är det

| | Var det bor | Går det att göra om? |
|---|---|---|
| **Bokningarna** | Docker-volymen `booking_booking-data` | Nej. Det finns ingen annan kopia |
| Hemligheter | `/srv/booking/.env` | Nej — `BOOKING_PASSWORD` och bottoken går att byta, inte hämta |
| Husets resurser | `config.yaml` | Ja, den ligger i repot |
| Programmet | `ghcr.io/kollektivhuset-rudbeckia/booking` | Ja, hämtas |

Bara det första är svårt att göra om, så det är det som aldrig rörs av en
utrullning.

## Var den står

```
/srv/booking/
    .env                  # hemligheter — bara på servern, aldrig i repot
    config.yaml           # husets resurser, skickas med varje utrullning
    docker-compose.yml    # hur den körs, skickas med varje utrullning
    DEPLOYED_SHA          # vilken commit som rullades ut
                          # + volymen booking_booking-data
```

Katalogen ägs av kontot `deploy`, samma konto som rullar ut registret och
middagarna. Framför containern står husets nginx och proxar
`booking.rudbeckia.nu` till `localhost:8081`.

Volymens namn kommer av katalogens namn plus namnet i `docker-compose.yml`.
Katalogen heter fortfarande `booking`, precis som den gjorde när sidan låg i
`/home/kitain/git/booking`, så volymen heter `booking_booking-data` både före
och efter flytten — databasen behövde varken kopieras eller importeras om.

## Hur den kommer in

Nyckeln ligger i organisationens `QUEBEC_SSH_KEY` och delas av husets tre
tjänster. Den når kontot `deploy` på quebec, och med den nyckeln kan kontot
göra exakt en sak. `authorized_keys` binder nyckeln till ett *forced command*:

```
command="/usr/local/bin/deploy",no-agent-forwarding,no-port-forwarding,no-pty,no-user-rc,no-X11-forwarding
```

Vad den andra änden än ber om kör ssh det skriptet. Inget skal, ingen scp,
ingen vidarebefordran. Skriptet ägs av root och går inte att skriva till från
`deploy`, så nyckeln kan inte heller peka om sig själv.

### Varför tjänsten står i ssh-kommandot

Ett forced command hänger på *nyckeln*, inte på repot. Eftersom nyckeln är
gemensam kan den alltså inte i sig säga vilken tjänst som ska startas om, och
därför skickar workflowet namnet som ssh-kommando:

```bash
tar -czf - config.yaml docker-compose.yml DEPLOYED_SHA \
  | ssh "$USER@$HOST" booking
```

Det körs inte som ett kommando. `sshd` lägger strängen i
`SSH_ORIGINAL_COMMAND` och kör skriptet ändå, och skriptet matchar den mot en
fast lista — `members`, `dinner`, `booking` — där varje gren sätter katalog,
container och port från literaler. Strängen kommer utifrån och används därför
aldrig för att bygga en sökväg. Allt annat avvisas i stället för att gissas
på, och ett okänt namn ekas inte tillbaka i loggen.

Baksidan av en gemensam nyckel är värd att säga rakt ut: varje repo i
organisationen som kommer åt hemligheten kan rulla ut vilken som helst av de
tre tjänsterna. Vill man inte det, är det nyckeln som ska delas upp — en per
tjänst, var och en bunden till sitt eget skript.

## Vad som skickas

Deploy-nycklar är avstängda för repot, så i stället för att ge servern en
GitHub-kredential den annars aldrig behöver tar utrullningen med sig det den
ska ha. Workflowet packar tre filer och skickar dem över samma ssh-kanal:

| Fil | Varför |
|---|---|
| `config.yaml` | husets resurser, som containern läser från disk |
| `docker-compose.yml` | hur den körs |
| `DEPLOYED_SHA` | vilken commit som rullades ut |

Skriptet packar upp **bara** de tre — de står uppräknade vid namn, vilket är
det som gör att ett arkiv inte kan skriva var det vill. `.env` finns bara på
servern och rörs aldrig av en utrullning.

Servern har alltså ingen GitHub-token och ingen väg till GitHub alls. Imagen
är publik, så `docker compose pull` behöver ingen inloggning.

## Kedjan

`Deploy to quebec` hänger på `workflow_run` från **Build and publish image**
och inte på pushen, så en utrullning kan aldrig hinna före bygget och starta
om på gårdagens image. Ett bygge som misslyckas rullas inte ut alls.
`concurrency` släpper igenom en utrullning i taget och avbryter aldrig en som
är i gång — en halvkörd utrullning kan lämna containern stoppad.

Utrullningen checkar ut den commit som *byggdes*, inte vad `main` har hunnit
bli under minuterna sedan dess.

## Hemligheter

De ligger på organisationen och gäller alla tre tjänsterna:

| Secret | Vad |
|---|---|
| `QUEBEC_SSH_KEY` | privata halvan av nyckeln som kör `/usr/local/bin/deploy` |
| `QUEBEC_HOST` | `ssh.rudbeckia.nu` |
| `QUEBEC_USER` | `deploy` |
| `QUEBEC_KNOWN_HOSTS` | värdnyckeln, så att utrullningen inte litar på vad som helst |

Ett repo som sätter en egen hemlighet med samma namn tar över den från
organisationen. Gör det bara om tjänsten också har en egen nyckel bunden till
ett eget skript — annars går utrullningen in med en nyckel vars forced command
startar om någon annans container.

## När något går fel

Utrullningen väntar på att `/healthz` svarar och avbryter med de sista
loggraderna om den aldrig gör det. Den rullar **inte** tillbaka av sig själv —
en container som inte startar lämnar den förra imagen kvar i registret, och
att välja version är ett beslut för en människa:

```bash
ssh ssh.rudbeckia.nu
cd /srv/booking
docker compose down                 # inget -v, det raderar bokningarna
sed -i 's/:latest/:sha-abc1234/' docker-compose.yml
docker compose up -d
```

Nästa utrullning skriver över `docker-compose.yml` igen, så en pinnad version
håller bara till dess. Ska den hålla, pinna den i repot.

En saknad Mattermost-anslutning är ingen misslyckad utrullning: en bottoken
som inte går att logga in med fäller starten och därmed körningen, men en sida
som körs *utan* bot fungerar. Skriptet skriver `NOTE: running without
Mattermost` i stället för att fälla körningen.

Vill du rulla ut för hand, eller igen efter en misslyckad körning:
**Actions → Deploy to quebec → Run workflow**.

## Säkerhetskopiering

Allt ligger i en fil. SQLite kör i WAL-läge, så en kopia av en igångvarande
databas kan sakna det senaste — och `.db-wal` är lika viktig som `.db`. Att
stoppa i några sekunder är enklare än att komma ihåg det:

```bash
cd /srv/booking
docker compose stop booking
docker run --rm -v booking_booking-data:/d -v /backup:/b alpine \
    tar czf /b/booking-$(date +%F).tgz -C /d .
docker compose start booking
```

## Skriptet på servern

`/usr/local/bin/deploy`, ägt av root, delat av alla tre tjänsterna. Att lägga
till en fjärde är en rot-ändring, vilket är rätt sorts tröskel.

```sh
#!/bin/sh
set -eu

case "${SSH_ORIGINAL_COMMAND:-}" in
    members)
        NAME=members; SVC=members; CONTAINER=members-rudbeckia; PORT=8082
        READY='every synchronisation target answered'
        UNREADY='a synchronisation target did not answer — see /synk'
        ;;
    dinner)
        NAME=dinner; SVC=dinners; CONTAINER=dinners-rudbeckia; PORT=8099
        READY='mattermost bot ready'
        UNREADY='running without Mattermost — lists are only written to the log'
        ;;
    booking)
        NAME=booking; SVC=booking; CONTAINER=booking-rudbeckia; PORT=8081
        READY='mattermost bot ready'
        UNREADY='running without Mattermost — confirmations are only written to the log'
        ;;
    '')
        echo "no service was named; the workflow must pass one as the ssh command" >&2
        exit 1
        ;;
    *)
        echo "not a service this key may deploy" >&2
        exit 1
        ;;
esac

DIR=/srv/$NAME
cd "$DIR"

echo "==> $NAME"

ALLOWED="config.yaml docker-compose.yml DEPLOYED_SHA"

if [ ! -t 0 ]; then
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    if cat > "$tmp/in.tgz" && [ -s "$tmp/in.tgz" ]; then
        echo "==> unpacking configuration"
        tar -xzf "$tmp/in.tgz" -C "$tmp" $ALLOWED 2>/dev/null || {
            echo "    the archive did not hold what was expected" >&2; exit 1; }
        for f in $ALLOWED; do
            [ -f "$tmp/$f" ] || continue
            if cmp -s "$tmp/$f" "$DIR/$f"; then
                echo "    $f unchanged"
            else
                cp "$tmp/$f" "$DIR/$f"
                echo "    $f updated"
            fi
        done
    fi
fi

[ -f DEPLOYED_SHA ] && echo "==> version $(cat DEPLOYED_SHA)"

echo "==> pulling the image"
docker compose pull --quiet "$SVC"

echo "==> restarting"
was=$(docker inspect "$CONTAINER" --format '{{.State.StartedAt}}' 2>/dev/null || echo none)
docker compose up -d "$SVC"
now=$(docker inspect "$CONTAINER" --format '{{.State.StartedAt}}' 2>/dev/null || echo none)

echo "==> waiting for it to answer"
i=0
while [ "$i" -lt 40 ]; do
    if curl -fsS --max-time 3 "http://localhost:$PORT/healthz" >/dev/null 2>&1; then
        echo "    healthy after ${i}s"
        if [ "$was" = "$now" ]; then
            echo "    already running this image; nothing was restarted"
            exit 0
        fi
        if docker compose logs --no-color --since 120s "$SVC" 2>&1 |
           grep -q "$READY"; then
            echo "    $READY"
        else
            echo "    NOTE: $UNREADY"
        fi
        exit 0
    fi
    i=$((i + 1))
    sleep 1
done

echo "    it never became healthy. Last log lines:" >&2
docker compose logs --no-color --tail 40 "$SVC" >&2
exit 1
```
