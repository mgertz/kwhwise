# kwhwise

kwhwise henter den aktuelle danske elspotpris og beregner et kvalificeret
estimat for den samlede pris pr. kWh. Resultatet publiceres via MQTT, saa det
kan bruges direkte i Home Assistant.

Projektet implementeres i Go. Go giver et lille statisk container-image, lavt
RAM-forbrug og en enkel standardmodel for HTTP, JSON, MQTT, YAML og timers.
Rust kunne give et tilsvarende eller lidt lavere footprint, men Go vurderes som
det bedste kompromis mellem footprint, udviklingshastighed og vedligeholdelse.

## Arkitektur

Projektet bruger en forenklet hexagonal architecture. Det giver mening her,
fordi prisberegningen ikke skal kende detaljerne i hverken API, MQTT eller
Docker-konfiguration.

Kernen skal indeholde:

- Prisberegning
- Tarifopslag
- Momsregler
- Domænemodeller for priser og tidsperioder

Kernen skal arbejde gennem porte for:

- Hentning af spotpriser
- Hentning af tarifdata
- Publicering af beregnede priser
- Tid og konfiguration

Adapters skal i foerste version levere:

- Energi Data Service som spotpris-adapter
- YAML/environment som konfigurations-adapter
- MQTT som output-adapter
- Systemuret som timer-adapter

Strukturen skal holdes enkel. Der skal ikke introduceres lag eller interfaces,
som ikke beskytter en reel udskiftelighed eller goer kerneberegningen lettere
at teste.

Projektet er generisk. NRGi er den foerste relevante elaftale, men navne og
beregningslogik maa ikke bindes til NRGi.

## Aktuel implementeringsstatus og lokal koersel

Go-programmet indlaeser og validerer konfiguration og profiler, henter dagens
spotpriser fra Energinet ved opstart og beregner den samlede variable kWh-pris
inklusive moms. Prisen og dens delkomponenter publiceres via MQTT Discovery.
Derefter venter det til naeste hele danske time.
Dagens validerede timepriser caches i hukommelsen, saa hver timeopdatering
ikke kraever et nyt HTTP-kald. Et nyt dansk doegn hentes ved foerste opdatering
efter dansk midnat.

Foerste profiler er `nrgi` som elleverandoer og `cerius` som netselskab for
C-kunder i DK2. Spotpriskilden understoetter DK1 og DK2; andre netprofiler skal
tilfoejes, foer de omraader kan bruges til en fuld prisberegning.

### Docker

Kopier `.env.example` til `.env`, og angiv din eksisterende MQTT-broker,
eventuelle credentials og det faktiske spotpristillaeg fra din aftale:

```bash
cp .env.example .env
docker compose up -d --build
docker compose logs -f kwhwise
```

Brokerens IP eller hostname skal kunne naas fra containeren. Et Docker-navn
som `mqtt` kraever, at kwhwise er tilsluttet brokerens Docker-netvaerk; du kan
ogsaa laegge kwhwise-servicen ind i din eksisterende Compose-stack.

Imaget bygger en statisk binaer til en `scratch`-container med CA-certifikater,
indbyggede tidszonedata og profiler. Det indeholder ingen shell eller Go-runtime
installation og koerer som en ikke-root bruger.

Start grundprogrammet med:

```bash
ELECTRICITY_PROVIDER=nrgi REGION=dk2 GRID_OPERATOR=cerius SPOT_PRICE_MARKUP_ORE=9 MQTT_HOST=localhost go run ./cmd/kwhwise
```

Stop med Ctrl+C. Den kompilerede binaer stopper ogsaa paent ved `SIGTERM`,
som Docker sender ved stop. I eksemplet skal der vaere en lokal MQTT-broker;
angiv ellers dens hostname/IP samt credentials via environment variables.

Kontroller koden med:

```bash
go test -race ./...
go vet ./...
```

Den valgfrie container-integrationstest bruger en midlertidig MQTT-broker og
det rigtige spotpris-API. Den kraever Docker og rydder sine egne containere og
netvaerk op:

```bash
docker build -t kwhwise:local .
docker pull eclipse-mosquitto:2.0
go test -tags=integration ./integration -v -timeout 90s
```

De almindelige tests bruger lokale HTTP-stubs og ingen eksterne API-opslag.

GitHub Actions validerer PR-titler som Conventional Commits og koerer Go-tests,
vet og builds til amd64/arm64. Ved merge til `main` gentages test/build,
hvorefter semantic-release opretter en ny version, hvis commits kraever det.
Kun en ny release udloeser Docker-publicering.

Ved version 1.0.0 publiceres fx `ghcr.io/mgertz/kwhwise:1.0.0`, `:1.0`, `:1`
og `:latest`. Versionsnummeret vises ogsaa i Home Assistant device-info.
Se [release-processen og foerste release](docs/releasing.md).
`compose.yaml` bygger et lokalt image; i din egen stack kan det erstattes med
registry-imaget, naar det er publiceret.

Den implementerede hexagonale opdeling er:

```text
cmd/kwhwise/                    Opstart, signaler og samling af dependencies
internal/config/                Environment-konfiguration
internal/domain/                Prisberegner, tariffer, moms og UTC-intervaller
internal/application/           Porte, doegncache og pris-snapshot med forecast
internal/adapters/energinet/     HTTP, JSON-validering og kvarter-til-time mapping
internal/adapters/profiledata/   YAML-profiler, validering og kompilerede satser
internal/adapters/mqttpublisher/ MQTT Discovery, state, availability og reconnect
internal/scheduler/             Opstartstrigger og afbrydelig timeplanlaegning
profiles/                       Versionsstyrede YAML-data indbygget i binaeren
```

## Scope for foerste version

Foerste version skal:

- Bruge timepriser (`PT1H`)
- Understoette `DK1` og `DK2`
- Hente spotpriser fra Energi Data Service, som bygger paa Nord Pool-data
- Beregne en estimeret samlet pris i DKK/kWh
- Understoette konfigurerbart spotpristillaeg
- Understoette netselskabsprofiler med tidsafhaengige tariffer
- Understoette transmissionstarif, systemtarif og elafgift
- Haandtere sommer- og vintertariffer
- Publicere MQTT Discovery-entiteter til Home Assistant
- Publicere dagens timepriser som forecast-data

Foerste version publicerer kun timepriser. Datakilden leverer dog
15-minutterspriser, som adapteren skal samle til timegennemsnit. Publicering
af `PT15M`-priser kan tilfoejes senere.

## Prisbegreber

Den beregnede pris bestaar af disse komponenter:

```text
spotpris
+ spotpristillaeg
+ nettarif
+ transmissionsnettarif
+ systemtarif
+ elafgift
+ moms
= estimeret samlet pris
```

Alle delpriser normaliseres internt til oere/kWh. MQTT-sensoren for den samlede
pris publiceres i DKK/kWh.

Spotprisen fra Energi Data Service leveres i DKK/MWh og skal omregnes:

```text
DKK/MWh / 10 = oere/kWh
```

Hver tarif skal kunne angive, om den allerede indeholder moms. Vi maa ikke
antage, at alle komponenter har samme momsstatus.

## Spotpriskilde

Den primare spotpriskilde er Energi Data Service-datasettet `DayAheadPrices`.
Endpoint og metadata er verificeret den 9. oktober 2026:

```text
https://api.energidataservice.dk/dataset/DayAheadPrices
https://api.energidataservice.dk/meta/dataset/DayAheadPrices
```

`Elspotprices` er det historiske dataset for tiden foer 1. oktober 2025 og
skal ikke bruges til aktuelle priser.

Relevante felter:

- `TimeUTC`
- `TimeDK`
- `PriceArea`
- `DayAheadPriceDKK` (DKK/MWh, spotpris eksklusive moms og tillaeg)
- `DayAheadPriceEUR` (EUR/MWh)

`PriceArea` mappes til projektets `REGION`-vaerdi (`DK1` eller `DK2`).

Eksempel paa et dansk doegn for DK2:

```text
https://api.energidataservice.dk/dataset/DayAheadPrices?start=2026-10-09&end=2026-10-10&filter=%7B%22PriceArea%22%3A%5B%22DK2%22%5D%7D&sort=TimeUTC&limit=100
```

API'ets `start` og `end` angives i dansk lokal tid. Start er inkluderet, og
slut er ekskluderet. `TimeUTC` returneres uden `Z`, men skal fortolkes som UTC.
Brug dette felt som entydig tidsnoegle, ogsaa ved overgang til vintertid.

Datasettet har 15-minuttersoploesning. Adapteren skal kontrollere, at alle fire
kvarterer er til stede, og beregne timeprisen som deres aritmetiske gennemsnit.
Et normalt doegn giver 24 timer, men sommertidsskift giver 23 eller 25 timer.
Manglende data maa ikke udfyldes med en opdigtet pris, og negative spotpriser
skal bevares.

DKK-prisen beregnes med valutakursen fra Danmarks Nationalbank. Energinet
oplyser, at den derfor kan afvige fra danske spotpriser vist andre steder.
Det passer med projektets maal om et kvalificeret estimat.

Data opdateres dagligt (`P1D`). Dagens priser caches allerede i programmet.
MQTT indeholder dagens fulde prisarray. Forhaandshentning af morgendagens
priser kan tilfoejes senere.
Energinets vejledning foreslaar opslag cirka hvert femte minut mellem
13:00 og 14:00 CET for morgendagens priser. Ved HTTP 429 skal serverens
angivne ventetid respekteres.

HTTP-klienten har en timeout paa 10 sekunder og kan afbrydes ved stop.
Ved HTTP 429 huskes `Retry-After`, saa et senere kald ikke rammer API'et foer
fristen. Mangler en gyldig header, bruges en cooldown paa fem minutter.
Fejl og ufuldstaendige doegn logges som en utilgaengelig spotpris; de caches
ikke som gyldige priser. Fejl genproeves efter 5 sekunder med fordoblet ventetid
op til fem minutter. En senere `Retry-After`-frist har forrang.

Nord Pools kommercielle API skal ikke vaere en forudsaetning for projektet.
Energi Data Service er den offentlige og praktiske datakilde for foerste
version. Dokumentation: https://www.energidataservice.dk/guides/api-guides

## Konfiguration

Konfigurationen skal kunne angives via environment variables, saa en bruger
kan starte projektet med en enkel Docker Compose-konfiguration.

Minimumskonfiguration:

```yaml
environment:
  ELECTRICITY_PROVIDER: nrgi
  REGION: DK2
  GRID_OPERATOR: cerius
  SPOT_PRICE_MARKUP_ORE: 9
  SPOT_PRICE_MARKUP_VAT: included
  MQTT_HOST: mqtt
  MQTT_PORT: 1883
  MQTT_USERNAME: ${MQTT_USERNAME}
  MQTT_PASSWORD: ${MQTT_PASSWORD}
  MQTT_DISCOVERY_PREFIX: homeassistant
  MQTT_BASE_TOPIC: kwhwise
  MQTT_CLIENT_ID: kwhwise
  MQTT_TLS: "false"
  TZ: Europe/Copenhagen
```

### REGION

`REGION` skal angives; der vaelges ikke et prisomraade automatisk.
Mulige vaerdier i foerste version (smaa bogstaver accepteres ogsaa):

```text
DK1
DK2
```

### ELECTRICITY_PROVIDER

Vaelger leverandoerprofilen, i foerste version `nrgi`. Profilen beskriver
prisomraader og default-momsstatus, mens kunden altid angiver sit eget tillaeg.

### MQTT

`MQTT_HOST` er obligatorisk. `MQTT_PORT` har default 1883; username og password
er valgfrie, afhaengigt af brokerens opsaetning. Passwords logges ikke.
`MQTT_TLS=true` aktiverer TLS med certifikatkontrol (typisk port 8883).

Ved flere installationer skal hver have sit eget `MQTT_CLIENT_ID` og
`MQTT_BASE_TOPIC`, eksempelvis `kwhwise_cottage`. Client-ID'et bruges ogsaa til
unikke Home Assistant-entity-ID'er. `MQTT_DISCOVERY_PREFIX` er normalt
`homeassistant` og skal passe til HA's MQTT-konfiguration.

### GRID_OPERATOR

`GRID_OPERATOR` vaelger den netselskabsprofil, der skal bruges til nettariffen.
Vaerdien skal angives og normaliseres til smaa bogstaver.
Eksempel:

```text
GRID_OPERATOR=cerius
```

Cerius er et netselskab og ikke en elleverandoer. Vaelg profil ud fra det
faktiske netselskab paa din adresse. Cerius-profilen er kun kompatibel med DK2.

Tarifprofiler skal ligge som versionsstyrede datafiler i projektet, for
eksempel:

```text
profiles/providers/nrgi.yaml
profiles/grids/cerius.yaml
profiles/dk/national.yaml
```

Profilerne kan opdateres med pull requests uden kodeaendringer. Se
[verificerede satser, kildehenvisninger og profilformat](docs/profiles.md).
Satserne opdateres hos brugeren ved at bygge/hente en ny container-version.

### SPOT_PRICE_MARKUP_ORE

Dette er elaftalens faste tillag pr. kWh, angivet i oere/kWh. For NRGi kan
vaerdien typisk vaere 9, men den skal vaere konfigurerbar og maa ikke vaere
hardcoded.

Vaerdien skal angives og vaere et endeligt, ikke-negativt tal. Angiv eksplicit
0 for et tilbud uden tillaeg. Decimaler angives med punktum, eksempelvis `9.25`.

`SPOT_PRICE_MARKUP_VAT` angiver beloebets momsstatus:

- `excluded`: Beloebet er eksklusive moms og skal paalaegges moms.
- `included`: Beloebet indeholder allerede moms.
- `none`: Beloebet er momsfrit og skal ikke paalaegges moms.

Hvis momsstatus udelades, bruges profilens default: `included` for NRGi.

Eksempel: 9 oere eksklusive 25 % moms bliver 11,25 oere inklusive moms.
9 oere angivet med `included` indgaar fortsat som 9 oere.

NRGi's offentlige produktside angiver, at alle viste priser er inklusive moms.
Det viste standardtillaeg paa 9 oere skal derfor konfigureres med `included`,
som i Compose-eksemplet ovenfor. En personlig aftale kan have andre satser;
brug beloeb og momsstatus fra den konkrete aftale. Det kundespecifikke
`totalRetail` paa 11,0125 oere fra de lokale eksempler er ikke det samme som
9 oere plus 25 % moms og er ikke verificeret som en standardpris.

Faste maanedsabonnementer indgaar ikke i den aktuelle kWh-pris i foerste
version. De kan senere publiceres som separate oplysninger eller fordeles
efter en eksplicit valgt metode.

## Tarifprofiler

Tarifmodellen skal beskrive tre uafhaengige dele:

1. Spotpristillaeg fra elaftalen, konfigureret via environment variables.
2. Nettarif fra det faktiske netselskab og kundens tilslutningskategori.
3. Nationale komponenter: transmissionstarif, systemtarif og elafgift.

NRGi er en elleverandoer, ikke en faelles netprofil for alle NRGi-kunder.
NRGi's side bruger KONSTANT som et eksempel, men netselskabet skal vaelges
ud fra kundens faktiske adresse eller regning.

Foerste version retter sig mod almindelige husholdninger, normalt kundegruppe
C. Kundegruppen skal staa i tarifprofilen, saa en erhvervstarif ikke bruges
ved en fejl. Andre grupper og saerlige afgiftsordninger er senere udvidelser.

En netprofil skal indeholde:

- Netselskab og kundegruppe
- Lokal tarif-tidszone (`Europe/Copenhagen` for danske profiler)
- Eksplicit enhed (`ore_per_kwh`) og momsstatus
- Prisperioder med startdato inkluderet og slutdato ekskluderet
- Kilde-URL og dato for kontrol af satserne
- Sommer-/vintersaesoner, adskilt fra prisernes gyldighedsperiode
- En doegnplan med tidsintervaller og satser for hver saeson

Saesoner gentages efter maaned og dag, eksempelvis sommer fra `04-01` til
`10-01`. En prisaendring fra januar eller midt paa aaret beskrives med en ny
prisperiode; den maa ikke tvinges ind som en ny saeson. Tarifsaeson og skift
af urets sommer-/vintertid er to forskellige ting.

Foerste version har samme doegnplan alle ugens dage. Weekend og helligdage
skal ikke vaere obligatoriske felter. Saerskilte dagtyper tilfoejes, hvis en
konkret senere profil kraever dem.

Illustrativt eksempel med OPDIGTEDE satser; dette er ikke en brugbar netprofil:

```yaml
schema_version: 1
operator: example
customer_group: C
regions: [DK2]
timezone: Europe/Copenhagen
unit: ore_per_kwh
vat: included

periods:
  - valid_from: "2026-01-01"
    valid_until: "2027-01-01"
    source: "https://example.com/tariffs"
    verified_at: "2026-10-10"
    seasons:
      summer:
        from: "04-01"
        until: "10-01"
        rates:
          - { from: "00:00", to: "06:00", value: 10 }
          - { from: "06:00", to: "17:00", value: 15 }
          - { from: "17:00", to: "21:00", value: 40 }
          - { from: "21:00", to: "24:00", value: 15 }
      winter:
        from: "10-01"
        until: "04-01"
        rates:
          - { from: "00:00", to: "06:00", value: 10 }
          - { from: "06:00", to: "17:00", value: 30 }
          - { from: "17:00", to: "21:00", value: 90 }
          - { from: "21:00", to: "24:00", value: 30 }
```

Nationale satser ligger separat i `profiles/dk/national.yaml`.
Hver komponent skal have egen momsstatus, kilde og gyldighedsperiode, da
afgifter og Energinet-tariffer kan aendres paa forskellige datoer.

Tal fra NRGi's `totalGrid` maa ikke kopieres direkte ind som nettarif.
Det er et samlet API-felt, og komponenterne og det konkrete netselskab skal
identificeres foerst. Ellers risikerer vi at taelle nationale tariffer og
afgifter dobbelt. Standardprofiler maa kun bruge kildeverificerede satser.

Loaderen afviser overlappende prisperioder/saesoner,
ufuldstaendige doegnplaner, ukendte enheder/momsstatusser og ugyldige tal.
I timeprisversionen skal tidsintervaller graense ved hele timer.
Opslag uden en gaeldende prisperiode skal give en tydelig fejl frem for at
bruge en udloebet eller opdigtet tarif.

Prisberegneren skal returnere en opdeling med spotpris, spotpristillaeg,
nettarif, transmissionstarif, systemtarif, elafgift, moms og samlet pris.
Hver komponent skal kunne vises eksklusive og inklusive moms. Bevar
beregningspraecisionen internt og afrund foerst ved visning/publicering.

Kilder til modelafklaringen:

- NRGi's prisopdeling og momsangivelse: https://nrgi.dk/privat/stroem/
- Radius' tarifsaesoner og doegnperioder: https://radiuselnet.dk/priser/alle/prisforklaring/
- N1's kundegrupper og gaeldende satser: https://n1.dk/priser-og-vilkar

## Moms og prisberegning

Prisberegningen skal bevare de enkelte komponenter separat. Det goer det
muligt at kontrollere estimatet og senere rette momsregler uden at aendre
datamodellen.

Beregneren skal som minimum kunne behandle disse momsstatusser:

```text
included
excluded
none
```

Den samlede pris skal vaere:

```text
sum af komponenter med moms inkluderet
+ sum af momspligtige komponenter eksklusive moms * 1,25
+ sum af momsfrie komponenter
```

Det endelige momsformat skal verificeres mod officielle tarifdokumenter og
elregninger. Projektet viser et kvalificeret estimat og er ikke en officiel
fakturaberegner.

## MQTT og Home Assistant

MQTT Discovery skal oprette generiske entiteter med prefixet `kwhwise`.

Foerste version publicerer:

```text
sensor.kwhwise_current_price
sensor.kwhwise_spot_price
sensor.kwhwise_spot_price_markup
sensor.kwhwise_grid_tariff
sensor.kwhwise_transmission_tariff
sensor.kwhwise_system_tariff
sensor.kwhwise_electricity_tax
sensor.kwhwise_vat
```

Den vigtigste entitet er:

```text
sensor.kwhwise_current_price
```

Den skal have:

- Vaerdi i DKK/kWh
- `state_class: measurement` og enhed `DKK/kWh` (ikke en almindelig valutakonto)
- Aktuel gyldighedsperiode
- Region og netselskab som attributter
- Spotpris og alle delkomponenter som attributter
- Dagens timepriser som forecast-data

Vaelg `sensor.kwhwise_current_price` som prisentity for netforbruget i HA's
Energy Dashboard. Komponent-sensorerne er i DKK/kWh inklusive deres moms;
VAT-sensoren viser den samlede momsdel. Alle beloeb eksklusive/inklusive moms
kan ogsaa ses i `components_ore`-attributten.

MQTT-topics med default-konfiguration:

```text
homeassistant/sensor/kwhwise/<sensor>/config  Discovery, retained, QoS 1
kwhwise/state                              Aktuel pris og dagens array, retained, QoS 1
kwhwise/availability                       online/offline, retained, QoS 1
```

Discovery, prisstate og availability beholdes som retained. HA opretter
entities asynkront efter discovery, saa en sen abonnent skal kunne modtage
den allerede publicerede pris uden at vente paa naeste time.

Tilgaengelighed kraever BADE brokerens `online`-status OG, at prisbeskedens
absolutte `valid_from`/`valid_until`-periode er gyldig. En gammel retained pris
genoplives derfor ikke ved HA-genstart, selv hvis HA's relative expiry-timer
genstartes. En afbrydelig timer i publisheren markerer ogsaa prisen offline
ved det absolutte udloeb, hvis en ny pris endnu ikke er publiceret.

Gyldig state genudsendes ved broker-genforbindelse og HA's birth-besked
(`homeassistant/status`). MQTT Last Will og normal nedlukning markerer
sensorerne offline.

Watts Live er ikke en del af projektet. Forbrug leveres allerede separat til
Home Assistant via Watts Live og MQTT.

## Forventet Docker-interface

Containeren skal koere som en langvarig, event-driven MQTT-klient. Den skal
ikke poll'e API'et konstant.

Tidszonen styres med den almindelige Docker environment variable `TZ`. Hvis
den ikke er angivet, bruges `Europe/Copenhagen`, da projektets primare
maalgruppe er danske installationer. Brugere i andre lande kan overskrive den
med eksempelvis `TZ=Europe/Stockholm`.

Tidszonedata er inkluderet i Go-programmet, saa denne adfaerd ogsaa virker i
et minimalt container-image. Danske tarifopslag bruger altid
`Europe/Copenhagen`, uafhaengigt af `TZ`.

Koerselsforloebet er:

1. Indlaes og valider konfiguration ved opstart
2. Hent spotpriser og relevante tarifdata
3. Beregn den samlede aktuelle pris
4. Publicer MQTT Discovery og prisdata
5. Beregn naeste hele time i tarifprofilens tidszone
6. Sov indtil denne tid
7. Gentag fra trin 2

Den normale cyklus koerer derfor ved `xx:00:00` lokal tid. Timeren skal
beregne naeste hele time ud fra det aktuelle klokkeslaet i stedet for at bruge
en fast 60-minutters ticker. Det undgaar drift og haandterer sommertid og
vintertid korrekt.

`TZ` bestemmer processens/loggenes tidszone. Selve prisopdateringen foelger
danske timegraenser, saa en anden visningstidszone ikke forsinker prisskift.

Naeste tidspunkt findes langs den faktiske tidslinje, saa den gentagne time
ved overgang til vintertid ikke springes over. Opdateringer koerer sekventielt;
hvis en opdatering tager lang tid, ventes paa naeste fremtidige hele time uden
at afvikle en serie forsinkede opdateringer. Timeren kan afbrydes ved stop.

Ved kortvarige fejl bruges retry med backoff. Ved manglende/udloebne priser
markeres sensorerne unavailable frem for at lade en gammel pris fortsaette.
MQTT-genforbindelse bruger kun senest beregnede data, hvis de stadig er gyldige.

Containeren skal opdatere data:

- Ved opstart
- Naar nye priser er tilgaengelige
- Periodisk ved fejl eller manglende data
- Med retry og tydelig status ved API- eller MQTT-fejl

En normal succesfuld opdatering forventes at tage faa sekunder. Processen skal
derefter blokere paa en timer og bruge naesten ingen CPU.

MQTT-credentials leveres via environment variables eller lokal `.env`, som
er gitignored. Direkte laesning af Docker secret-filer er endnu ikke tilfoejet.

## Begraensninger og senere udvidelser

Foerste version rammer ikke noedvendigvis elregningen paa sidste decimal. Den
skal i stedet levere et gennemsigtigt og kvalificeret estimat.

Mulige senere udvidelser:

- 15-minutterspriser
- Flere elhandlere og spotpristillaeg
- Flere netselskaber
- Helligdagsregler
- Faste abonnementer
- Historisk prisdatabase
- Automatisk valg af netselskab via adresse eller DataHub-oplysninger
- Sammenligning mellem beregnet pris og faktisk elregning
