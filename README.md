# kwhwise

kwhwise skal hente den aktuelle danske elspotpris og beregne et kvalificeret
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

15-minutterspriser er ikke en del af foerste version. Datamodellen skal dog
ikke forhindre, at `PT15M` kan tilfoejes senere.

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

Spotprisen fra Energi Data Service leveres typisk i DKK/MWh og skal omregnes:

```text
DKK/MWh / 10 = oere/kWh
```

Hver tarif skal kunne angive, om den allerede indeholder moms. Vi maa ikke
antage, at alle komponenter har samme momsstatus.

## Spotpriskilde

Den primare spotpriskilde er Energi Data Service-datasettet `Elspotprices`:

```text
https://api.energidataservice.dk/dataset/Elspotprices
```

Relevante felter er blandt andet:

- `HourUTC`
- `HourDK`
- `PriceArea`
- `SpotPriceDKK`

`PriceArea` mappes til projektets `REGION`-vaerdi (`DK1` eller `DK2`).

Nord Pools kommercielle API skal ikke vaere en forudsaetning for projektet.
Energi Data Service er den offentlige og praktiske datakilde for foerste
version.

## Konfiguration

Konfigurationen skal kunne angives via environment variables, saa en bruger
kan starte projektet med en enkel Docker Compose-konfiguration.

Minimumskonfiguration:

```yaml
environment:
  REGION: DK2
  GRID_OPERATOR: konstant
  SPOT_PRICE_MARKUP_ORE: 9
  MQTT_HOST: mqtt
  MQTT_PORT: 1883
  MQTT_USERNAME: ${MQTT_USERNAME}
  MQTT_PASSWORD: ${MQTT_PASSWORD}
  MQTT_DISCOVERY_PREFIX: homeassistant
  MQTT_BASE_TOPIC: kwhwise
  TZ: Europe/Copenhagen
```

### REGION

Mulige vaerdier i foerste version:

```text
DK1
DK2
```

### GRID_OPERATOR

`GRID_OPERATOR` vaelger den netselskabsprofil, der skal bruges til nettariffen.
Eksempel:

```text
GRID_OPERATOR=konstant
```

KONSTANT er et netselskab og ikke en region. En bruger skal kun vaelge denne
profil, hvis adressen ligger i KONSTANTs netomraade.

Tarifprofiler skal ligge som versionsstyrede datafiler i projektet, for
eksempel:

```text
tariffs/konstant.yaml
tariffs/radius.yaml
tariffs/n1.yaml
```

Profilerne skal kunne opdateres med pull requests uden kodeaendringer.

### SPOT_PRICE_MARKUP_ORE

Dette er elaftalens faste tillag pr. kWh, angivet i oere/kWh. For NRGi kan
vaerdien typisk vaere 9, men den skal vaere konfigurerbar og maa ikke vaere
hardcoded.

Faste maanedsabonnementer indgaar ikke i den aktuelle kWh-pris i foerste
version. De kan senere publiceres som separate oplysninger eller fordeles
efter en eksplicit valgt metode.

## Tarifprofiler

En tarifprofil skal kunne indeholde:

- Sommer- og vintersaeson
- Gyldighedsdatoer
- Hverdag og weekend
- Tidsintervaller i lokal tid
- Tarifvaerdi i oere/kWh
- Oplysning om moms er inkluderet
- Eventuelt helligdage i en senere version

Eksempel paa struktur:

```yaml
operator: konstant
timezone: Europe/Copenhagen

seasons:
  winter:
    valid_from: "2026-10-01"
    valid_until: "2027-04-01"
    weekday:
      - from: "00:00"
        to: "06:00"
        value: 29.792
        vat: included
      - from: "06:00"
        to: "17:00"
        value: 58.626
        vat: included
      - from: "17:00"
        to: "21:00"
        value: 145.128125
        vat: included
      - from: "21:00"
        to: "24:00"
        value: 58.626
        vat: included
```

De konkrete tarifvaerdier skal valideres mod den aktuelle tarifinformation
fra netselskabet foer de bruges som standardprofiler.

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
+ sum af komponenter uden moms * 1,25
+ komponenter uden moms
```

Det endelige momsformat skal verificeres mod officielle tarifdokumenter og
elregninger. Projektet viser et kvalificeret estimat og er ikke en officiel
fakturaberegner.

## MQTT og Home Assistant

MQTT Discovery skal oprette generiske entiteter med prefixet `kwhwise`.

Foerste version skal som minimum publicere:

```text
sensor.kwhwise_current_price
sensor.kwhwise_spot_price
sensor.kwhwise_spot_price_markup
sensor.kwhwise_grid_tariff
sensor.kwhwise_transmission_tariff
sensor.kwhwise_system_tariff
sensor.kwhwise_electricity_tax
```

Den vigtigste entitet er:

```text
sensor.kwhwise_current_price
```

Den skal have:

- Vaerdi i DKK/kWh
- `device_class: monetary`
- Aktuel gyldighedsperiode
- Region og netselskab som attributter
- Spotpris og alle delkomponenter som attributter
- Dagens timepriser som forecast-data

Watts Live er ikke en del af projektet. Forbrug leveres allerede separat til
Home Assistant via Watts Live og MQTT.

## Forventet Docker-interface

Containeren skal koere som en langvarig, event-driven MQTT-klient. Den skal
ikke poll'e API'et konstant.

Tidszonen styres med den almindelige Docker environment variable `TZ`. Hvis
den ikke er angivet, bruges `Europe/Copenhagen`, da projektets primare
maalgruppe er danske installationer. Brugere i andre lande kan overskrive den
med eksempelvis `TZ=Europe/Stockholm`.

Koerselsforloebet er:

1. Indlaes og valider konfiguration ved opstart
2. Hent spotpriser og relevante tarifdata
3. Beregn den samlede aktuelle pris
4. Publicer MQTT Discovery og prisdata
5. Beregn naeste lokale hele time i `Europe/Copenhagen`
6. Sov indtil denne tid
7. Gentag fra trin 2

Den normale cyklus koerer derfor ved `xx:00:00` lokal tid. Timeren skal
beregne naeste hele time ud fra det aktuelle klokkeslaet i stedet for at bruge
en fast 60-minutters ticker. Det undgaar drift og haandterer sommertid og
vintertid korrekt.

Ved kortvarige fejl skal klienten bruge retry med backoff. En fejl maa ikke
stoppe containeren. Den senest kendte gyldige pris skal beholdes som retained
MQTT-state, mens sensorens status kan markeres som stale.

Containeren skal opdatere data:

- Ved opstart
- Naar nye priser er tilgaengelige
- Periodisk ved fejl eller manglende data
- Med retry og tydelig status ved API- eller MQTT-fejl

En normal succesfuld opdatering forventes at tage faa sekunder. Processen skal
derefter blokere paa en timer og bruge naesten ingen CPU.

Secrets som MQTT-passwords maa kun leveres via environment variables eller
Docker secrets og maa ikke ligge i Git.

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
