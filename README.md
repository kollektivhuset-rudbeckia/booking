# booking

Bokningssystem för Kollektivhuset Rudbeckia. Husets medlemmar bokar cyklar,
gästrum och lokaler bakom ett gemensamt lösenord, får en bekräftelse som
direktmeddelande från husets Mattermost-bot och kan lägga in bokningen i sin
egen kalender. Sidan finns på svenska och engelska.

Byggt som en enda statisk Go-binär med SQLite. Ingen databasserver, ingen
byggkedja för frontend, inget att uppdatera utöver containern.

---

## Prova på

Vill du bara se hur det ser ut? Starta en demo. Den behöver ingen konfiguration,
inga lösenord och ingen databas — och den fyller sig själv med påhittade
bokningar så att du har något att klicka på.

**Med Docker** — det enda du behöver är Docker:

```bash
docker run --rm -p 8080:8080 -e DEMO=true \
    ghcr.io/mikaelo/booking.rudbeckia.nu:latest
```

**Med Go** — om du klonat repot:

```bash
make demo          # eller: go run ./cmd/server -demo
```

**Med repot och Docker** — bygger från källkoden om bilden inte går att hämta:

```bash
docker compose -f docker-compose.demo.yml up
```

Öppna sedan <http://localhost:8080>.

| | |
|---|---|
| Lösenord | `demo` — redan ifyllt i formuläret |
| Administratör | `admin` — visar allas bokningar under **Alla bokningar** |

Demon säger tydligt ifrån med en banner på varje sida, och all data ligger i
minnet: stoppar du containern är allt borta. Kör aldrig `DEMO=true` skarpt —
lösenorden står ju på sidan.

Saker att testa: boka en cykel och se hur tiderna runtomkring stängs av
[pausen mellan bokningar](#alla-parametrar), försök boka samma tid två gånger,
boka ett gästrum över några nätter, ladda ner kalenderfilen från bekräftelsen,
och logga in som `admin` för att se hela husets bokningar och exportera dem.

---

## Kom igång på riktigt

```bash
cp .env.example .env
$EDITOR .env                 # sätt åtminstone BOOKING_PASSWORD
docker compose up -d
```

Sidan ligger på <http://localhost:8080>. Logga in med `BOOKING_PASSWORD`.

Vill du köra utan Docker:

```bash
BOOKING_PASSWORD=hemligt go run ./cmd/server
```

Alla vardagskommandon finns i `Makefile` — kör `make` för att se dem.

---

## Boten i Mattermost

Bekräftelser och avbokningar går som direktmeddelanden från ett bot-konto, och
samma konto används för att slå upp vem i huset som bokar. Så här kopplar du in
det:

1. I Mattermost: **System Console → Integrations → Bot Accounts**, slå på dem.
2. **Integrations → Bot Accounts → Add Bot Account**. Kalla den `booking`.
3. Kopiera token som visas *en gång* och lägg den i `.env` som
   `MATTERMOST_TOKEN`. Sätt `MATTERMOST_URL` till husets adress.
4. Boten behöver få slå upp användare och skicka direktmeddelanden. Ett vanligt
   bot-konto räcker; den behöver inte vara systemadministratör.
5. Starta om: `docker compose up -d`. Loggen säger `mattermost bot ready` med
   botens användarnamn om token fungerar, och vägrar starta om den inte gör det.

```bash
# .env
MATTERMOST_URL=https://chat.rudbeckia.nu
MATTERMOST_TOKEN=...
# Medan huset provar: bara de här användarnamnen får boka. Tom = alla.
MATTERMOST_ALLOW=mikael.ostberg
```

Utan `MATTERMOST_URL` och `MATTERMOST_TOKEN` fungerar sajten ändå: bokningar
går igenom, användarnamnet sparas som det skrivs och bekräftelsen skrivs i
loggen i stället för att skickas. Det är också vad demoläget gör — demon når
aldrig en riktig chattserver.

---

## Två språk

Hela sajten finns på svenska och engelska. Flaggan uppe till höger byter, och
valet minns i ett år i en kaka. Har man inte valt något gäller webbläsarens
`Accept-Language`, och säger den ingenting vi förstår används `site.language`
ur `config.yaml`.

Orden ligger i `internal/i18n/catalog.go`, två språk sida vid sida på varje rad:

```go
"login.password": {"Husets lösenord", "The house password"},
"error.toofar":   {"Du kan boka högst %s i förväg.", "You can book at most %s ahead."},
```

Mallar och kod slår upp dem med nyckeln — `{{t "login.password"}}` respektive
`i18n.T(lang, "error.toofar", …)`. En nyckel som saknas renderas som `⟦nyckel⟧`
i stället för ett tomrum, men den ska aldrig hinna dit: testerna läser mallarna
och koden och underkänner bygget om någon nyckel saknas, om en översättning är
tom, om `%s` inte står i samma ordning i båda språken, eller om det ligger kvar
svensk text i en mall.

Datum och tider följer med: `tisdag 18 augusti` blir `Tuesday 18 August`, och
`3 månader` blir `3 months`.

**Husets egna ord** — namnen, beskrivningarna och instruktionerna för det som
går att boka — står i `config.yaml`, inte i katalogen. Varje sådant fält har en
`_en`-syskonrad:

```yaml
- id: ellastcykel
  name: Ellastcykeln
  name_en: The electric cargo bike
  description: Elektrisk lastcykel med plats för barn eller storhandling.
  description_en: Electric cargo bike with room for children or a big shop.
```

Utelämnar du `_en` visas svenskan även på den engelska sidan. En halv
översättning är bättre än ett tomrum: läsaren förstår ändå vad huset menar.

**Botens meddelanden** går på mottagarens eget språk, det som står i hens
Mattermost-konto — inte i det språk sidan råkade vara inställd på när bokningen
gjordes. Meddelandet dyker upp i chatten, kanske dagar senare, på chattens
villkor. Språket sparas med bokningen, så en avbokning långt efteråt talar
samma språk utan att fråga Mattermost igen. Säger kontot ingenting vi förstår
används `site.language`.

---

## Vad som går att boka

Allt bokningsbart står i `config.yaml`. Filen läses vid start, så starta om
containern när du ändrat något. Kontrollera först att filen är giltig:

```bash
docker compose run --rm booking -check-config
```

### Kategorier

Kategorier grupperar resurserna på startsidan och kan peka på den kanal där
huset pratar om just de sakerna:

```yaml
categories:
  - id: cyklar
    name: Cyklar
    emoji: 🚲
    description: Husets gemensamma cyklar. Ladda gärna batteriet efter din tur.
    link: https://chat.rudbeckia.nu/rudbeckia/channels/cykelpoolen
    link_text: "#cykelpoolen i Mattermost"
```

### Två sorters resurser

**`mode: hours`** – saker man lånar en stund, som cyklar. Medlemmen väljer dag,
längd och starttid ur ett rutnät.

```yaml
- id: ellastcykel
  category: cyklar
  name: Ellastcykeln
  emoji: 🚲
  description: Elektrisk lastcykel med plats för barn eller storhandling.
  location: Cykelrummet i källaren
  instructions: Nycklarna hänger i städrummet intill hissen.
  info_url: https://rudbeckia.nu/huset/cykelrummet/   # "Läs mer"-länk
  booking:
    mode: hours
    durations: [1, 2, 4, 8]        # snabbvalen som visas som knappar
    custom_duration: true          # låt folk skriva in en egen längd
    min_duration_minutes: 30       # kortaste egna längd
    max_duration_minutes: 600      # längsta egna längd
    slot_step_minutes: 30          # starttider ligger på halvtimmen
    buffer_minutes: 15             # tvingande lucka mellan två bokningar
    open_from: "06:00"
    open_to: "22:00"
    max_advance_days: 30
    min_notice_minutes: 0          # hur nära inpå man får boka
    max_active_per_user: 2         # samtidiga bokningar per person
    max_hours_per_week_per_user: 16
```

Med `custom_duration: true` står knapparna kvar som snabbval, och bredvid dem
dyker en knapp upp som heter **Egen längd**. Klickar man på den — precis som på
vilket snabbval som helst — fälls ett fält ut där man skriver sin egen längd,
`3` eller `1,5`. Tiderna under uppdateras medan man skriver. Utan JavaScript
finns en vanlig **Visa tider**-knapp i stället, och resultatet blir detsamma. Längden måste hålla sig mellan `min_duration_minutes` och
`max_duration_minutes` och gå jämnt ut i `slot_step_minutes`. Snabbvalen
fungerar alltid, även om de skulle ligga utanför de gränserna.

Utan `custom_duration` går bara längderna i `durations` att boka, precis som
förut.

**`mode: days`** – saker man bokar över natten, som gästrum. Medlemmen väljer
in- och utcheckningsdatum i en månadskalender.

```yaml
- id: gastrum-1
  category: gastrum
  name: Gästrum 1
  booking:
    mode: days
    min_days: 1                    # kortaste bokning i nätter
    max_days: 7
    check_in: "15:00"
    check_out: "12:00"
    max_advance_days: 90
    max_active_per_user: 2
```

### Sidans egna inställningar

`site` överst i filen styr rubrik, tidszon och språk:

```yaml
site:
  title: Rudbeckia bokning
  language: sv          # sv eller en: vad en besökare möts av innan hen väljer
  tagline: Kollektivhuset Rudbeckia
  tagline_en: Rudbeckia housing co-operative
  timezone: Europe/Stockholm
```

### Lägga till en ny resurs

Lägg till ett block under `resources`, med ett `id` som aldrig ändras (det står
i länkar och i databasen). Sätt `enabled: false` för att dölja något utan att ta
bort det — bokningar som redan finns påverkas inte.

`config.yaml` innehåller redan matsalen och snickarverkstaden avstängda, som
utgångspunkt när huset vill boka fler saker digitalt.

### Alla parametrar

| Nyckel | Gäller | Betyder |
|---|---|---|
| `name_en`, `description_en`, `location_en`, `instructions_en` | båda | engelska varianter. Utelämnade visas svenskan |
| `mode` | båda | `hours` eller `days` |
| `durations` | hours | snabbvalen, i timmar |
| `custom_duration` | hours | låter den som bokar skriva in en egen längd |
| `min_duration_minutes`, `max_duration_minutes` | hours | gränser för en egen längd. Standard: ett slotsteg respektive det längsta snabbvalet |
| `slot_step_minutes` | hours | rutnätet starttider ligger på, och steget en egen längd måste gå jämnt ut i |
| `open_from`, `open_to` | hours | den del av dygnet som får bokas |
| `min_days`, `max_days` | days | kortaste och längsta bokning i nätter |
| `check_in`, `check_out` | days | klockslag för in- och utcheckning |
| `buffer_minutes` | båda | tvingande fri tid mellan två bokningar |
| `max_advance_days` | båda | hur långt fram i tiden man får boka |
| `min_notice_minutes` | båda | hur nära inpå man får boka |
| `max_active_per_user` | båda | samtidiga bokningar per Mattermost-konto |
| `max_hours_per_week_per_user` | hours | takt-tak per person, 0 = inget tak |

---

## Inställningar

Allt hemligt sätts som miljövariabler, aldrig i `config.yaml`. Se
[`.env.example`](.env.example) för hela listan.

| Variabel | Standard | Betyder |
|---|---|---|
| `BOOKING_PASSWORD` | – | **Krävs.** Husets gemensamma lösenord |
| `BASE_URL` | `http://localhost:8080` | Adressen som hamnar i botens meddelanden och kalenderlänkar |
| `ADMIN_PASSWORD` | tom | Låser upp `/admin`. Tom = ingen adminvy |
| `SESSION_SECRET` | härleds | Signerar sessionskakor |
| `SESSION_DAYS` | `30` | Hur länge en inloggning håller |
| `CONFIG_PATH` | `/config.yaml` | Var resursfilen ligger |
| `DB_PATH` | `/data/booking.db` | Var databasen ligger |
| `LISTEN_ADDR` | `:8080` | Adress att lyssna på |
| `TRUST_PROXY` | `true` | Läs klientens IP ur `X-Forwarded-For` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` eller `error` |
| `MATTERMOST_URL` | tom | Husets Mattermost, t.ex. `https://chat.rudbeckia.nu` |
| `MATTERMOST_TOKEN` | tom | Bot-kontots access token. Utan URL och token loggas bekräftelserna i stället för att skickas |
| `MATTERMOST_ALLOW` | tom | Kommaseparerade användarnamn som får boka. Tom = alla i katalogen |
| `DEMO` | `false` | Demoläge: lösenorden `demo`/`admin`, exempelbokningar och en banner. Aldrig i skarp drift |

Sätter du inte `SESSION_SECRET` härleds den ur lösenorden. Det betyder att ett
byte av husets lösenord loggar ut alla — praktiskt när någon flyttar ut.

---

## Så fungerar det

**Ett lösenord, inga konton.** Hela sajten ligger bakom `BOOKING_PASSWORD`.
Vem som bokar väljs ur husets Mattermost-katalog: medlemmen söker på sitt namn
eller sitt användarnamn — **Anna Andersson**, **Östberg** och
**anna.andersson** hittar alla rätt person — och får förslag medan hen skriver.
Namn och e-post hämtas från kontot, så det finns inget att skriva fel. Valet
sparas i en signerad kaka så nästa bokning går fortare. `ADMIN_PASSWORD` ger
dessutom `/admin` med alla bokningar, filter och CSV-export.

**Sökningen sker i webbläsaren.** Listan över dem som får boka hämtas en gång
när formuläret öppnas och indexeras i ett trie: varje ord i varje namn och
användarnamn pekar på de personer det hör till, så ett prefix går direkt till
sina träffar utan att röra någon annan. Ingen förfrågan per tangenttryck, och
träffarna kommer innan man hunnit släppa tangenten. Accenter faldas åt båda
hållen, och flera ord måste alla stämma — *mikael öst* hittar Mikael Östberg
men inte Mikael Ek. Är huset större än tusen konton skickas listan inte alls;
då söker servern i stället, och fältet fungerar precis som förut.

Utan JavaScript är fältet en vanlig textruta: skriv användarnamnet, eller hela
namnet, och servern slår upp det. Två personer med samma namn får ett
felmeddelande som räknar upp bägges användarnamn i stället för en gissning.

**Bekräftelse i Mattermost.** När bokningen gått igenom skickar boten ett
direktmeddelande med en bifogad `.ics`-fil (Apple Calendar, Outlook,
Thunderbird) och en direktlänk till Google Calendar. Meddelandet innehåller
också länken för att avboka. Avbokningar skickas på samma sätt.

**Bara boten pratar utåt.** Kommunikationen går bara i en riktning: boten
skickar, den lyssnar inte. Det finns ingen inkommande webhook och inget slash
command att konfigurera, och därmed ingen ny väg in i huset.

**Vem som får boka.** `MATTERMOST_ALLOW` begränsar bokning till en lista
användarnamn medan huset provar systemet. Användarväljaren visar bara dem som
får boka, så den kan inte föreslå någon som formuläret sedan nekar. Tom lista
betyder att alla i katalogen får boka.

**Vem har bokat vad.** Varje resurs har en sida med alla kommande bokningar i
tidsordning, på `/resurs/<id>/bokningar`, länkad från startsidan och från
bokningssidan. Passerade bokningar visas inte — sidan svarar på frågan "när är
den ledig?", inte "vem lånade den i mars?".

**Kalenderflöden.** Varje resurs publicerar sina bokningar på
`/kalender/<id>/flode.ics`, om huset vill visa dem i en gemensam kalender.

**Dubbelbokning är omöjlig.** Kollisionskontrollen och skrivningen sker i samma
transaktion, med `BEGIN IMMEDIATE`, så två personer som klickar på samma tid i
samma sekund inte båda kan vinna. Den som förlorar får ett vänligt felmeddelande
och behåller sina ifyllda uppgifter.

---

## Bakom en proxy

Sätt `BASE_URL` till den riktiga adressen — den styr länkarna i botens
meddelanden, och den
avgör om sessionskakan sätts som `Secure` (den blir det när adressen är
`https://`). Exempel för Caddy:

```
booking.rudbeckia.nu {
    reverse_proxy booking:8080
}
```

Nginx behöver skicka vidare `X-Forwarded-For` för att inloggningsspärren ska
räkna rätt IP-adress.

---

## Deploy

Varje push till `main` bygger en image och lägger den i GitHub Packages, för
både `amd64` och `arm64`. Testerna körs först — går de inte igenom publiceras
ingenting.

| Tagg | Sätts när |
|---|---|
| `latest` | vid varje bygge från huvudgrenen, och vid en skarp version (`v1.2.3`) |
| `main` | vid bygge från huvudgrenen |
| `1.2.3`, `1.2` | vid en `v1.2.3`-tagg |
| `sha-abc1234` | vid varje bygge, om du vill låsa en deploy till en exakt commit |

En förhandsversion (`v1.2.3-rc1`) flyttar medvetet inte `latest`. Bygget
kontrollerar själv att `latest` verkligen kom med, så regeln inte tappas bort
vid en framtida ändring.

```
ghcr.io/mikaelo/booking.rudbeckia.nu:latest
```

Uppdatera på servern:

```bash
docker compose pull && docker compose up -d
```

Första gången du hämtar från ett privat paket:

```bash
echo $GITHUB_TOKEN | docker login ghcr.io -u <användarnamn> --password-stdin
```

---

## Säkerhetskopiering

Allt ligger i en enda SQLite-fil i volymen `booking-data`.

```bash
docker compose exec booking /booking -version   # kolla att den lever
docker run --rm -v booking-data:/data -v "$PWD:/backup" alpine \
    cp /data/booking.db /backup/booking-$(date +%F).db
```

Databasen körs i WAL-läge, så ta med `-wal`-filen om du kopierar en igång
varande databas — eller stoppa containern först.

---

## Utveckling

```bash
go test ./...                        # alla tester
go test -race ./...                  # med kapplöpningsdetektor
go vet ./...
make test-js                         # sökningen i webbläsaren (kräver Node)
BOOKING_PASSWORD=hemligt ADMIN_PASSWORD=admin go run ./cmd/server
```

Det finns ingen byggkedja för frontend. `members.js` är ett vanligt skript utan
importer, så testerna laddar det med en attrapp för `window` och läser vad det
exporterade — Node behövs bara om du vill köra just dem.

| Paket | Ansvar |
|---|---|
| `internal/config` | Läser `config.yaml` och miljövariabler |
| `internal/store` | SQLite: bokningar, kollisioner, sökning |
| `internal/booking` | Räknar ut lediga tider och validerar en bokning |
| `internal/auth` | Lösenordsgrind, signerade kakor, tokens |
| `internal/i18n` | Ordkatalogen, datum och språkvalet |
| `internal/mattermost` | Bot-klient: användarkatalog och direktmeddelanden |
| `internal/ical` | `.ics`-filer och kalenderlänkar |
| `internal/web` | Rutter, HTML-mallar, CSS |
| `internal/demo` | Exempelbokningar för demoläget |

Mallar och statiska filer bäddas in i binären med `go:embed`, så det finns bara
en fil att flytta runt. CSS och JavaScript länkas med en hash av innehållet i
adressen (`/static/app.js?v=…`), så en ny version når webbläsarna direkt i
stället för att ligga kvar i deras cache en timme.
