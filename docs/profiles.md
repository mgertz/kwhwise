# Tarifprofiler

Profilerne ligger i `profiles/` og indbygges i binaeren. Runtime kontakter kun
Energinets spotpris-API og MQTT-brokeren. Aendringer i profilerne leveres via en
ny build/container-version, ikke ved dynamisk download af kode fra GitHub.

## Understoettet nu

- Elleverandoer: `nrgi` (variabel spotprisaftale, med brugerens eget tillaeg).
- Netselskab: `cerius`, kundegruppe C, DK2.
- Nationale tariffer: almindeligt dansk husholdningsforbrug.

Faste abonnementer, solcelleindfoedning, effektbetaling og saerlige
afgiftsrefusioner indgaar ikke i den variable importpris.

## Verificerede satser

Kontrolleret 10. oktober 2026. Beloebene her er oere/kWh **eksklusive moms**:

| Komponent | Periode | Nat 00-06 | Dag 06-17 / 21-24 | Spids 17-21 |
|---|---|---:|---:|---:|
| Cerius C, sommer | 2026-04-01 til 2026-10-01 | 11,5336 | 17,3004 | 44,9811 |
| Cerius C, vinter | 2026-10-01 til 2027-04-01 | 11,5336 | 34,6008 | 103,8025 |
| Transmission | 2026-01-01 til 2027-01-01 | 4,3 | 4,3 | 4,3 |
| Systemtarif | 2026-01-01 til 2027-01-01 | 7,2 | 7,2 | 7,2 |
| Elafgift | 2026-01-01 til 2028-01-01 | 0,8 | 0,8 | 0,8 |

Slutdatoer er eksklusive. Den fulde beregning har derfor aktuelt verificeret
daekning fra **2026-04-01 til 2027-01-01**. Efter dette skal nationale satser
opdateres; programmet bruger ikke tavst udloebne satser.

Med moms giver Cerius + de nationale komponenter om vinteren:

- Nat: 29,792 oere/kWh.
- Dag/aften: 58,626 oere/kWh.
- Spids: 145,128125 oere/kWh.

Det matcher de tre samlede gridbeloeb i det undersoegte NRGi-kontosvar.
Der er ikke kopieret kundeidentifikatorer eller private API-URL'er til profilerne.

## Kilder og moms

Satserne er verificeret via [DataHub Price List](https://api.energidataservice.dk/meta/dataset/DatahubPricelist).
Kildens enhed er DKK/kWh, som omregnes til oere/kWh ved at gange med 100.

- Cerius: GLN `5790000705184`, kode `30TR_C_ET`.
- Nationale satser: GLN `5790000432752`, koder `40000`, `41000`, `EA-001`.
- De praecise forespoergsler og kontroltidspunktet findes i YAML-filerne.

DataHub oplyser, at `VATClass` er vejledende og kan vaere fejlbehaeftet.
Momsbehandlingen er derfor ogsaa krydstjekket med NRGi's prisopdeling, hvor
transmission vises som 5,38 oere, systemtarif som 9 oere og elafgift som 1 oere
inklusive moms, samt de samlede gridbeloeb ovenfor. Internt bruges de
uafrundede satser, ikke den afrundede visning fra hjemmesiden.

NRGi's hjemmeside angiver alle viste priser inklusive moms. Den dokumenterede
default i leverandoerprofilen er derfor `included`. Det faktiske tillaeg
kommer altid fra `SPOT_PRICE_MARKUP_ORE`, og `SPOT_PRICE_MARKUP_VAT` kan
overskrive profilens default. En personlig aftale kan afvige fra hjemmesiden.

## Tilfoej en ny profil

En ny elleverandoer tilfoejes i `profiles/providers/<id>.yaml`. Den beskriver
identitet, prisomraader, enhed, default-momsstatus for tillaegget og kilde.
Brugeren indtaster fortsat sit eget tillaeg; en leverandoer kan have flere
aftaler og tilbud, saa vi gaetter ikke kundens sats.

Et nyt netselskab tilfoejes i `profiles/grids/<id>.yaml`. Brug Cerius-filen som
struktur-eksempel, men hent de faktiske satser fra den nye netprofil.
Filer med navne, der matcher embed-moenstrene, indbygges automatisk.

Loaderen validerer:

- Schema-version, enhed, kundegruppe og kompatibelt prisomraade.
- Momsstatus, endelige ikke-negative satser, kilder og kontroltidspunkter.
- Konkrete prisperioder uden overlap.
- Saesoner uden overlap eller huller, ogsaa paa skuddagen.
- Praecis daekning af alle 24 lokale klokketimer med hele timegraenser.
- At de tre nationale komponenter findes og ikke taelles dobbelt.

Hvert opslag bruger den gaeldende prisperiode, saeson og lokale klokketid.
Begge forekomster af kl. 02 ved overgang til vintertid bruger samme nettarif,
men beholder hver sin spotpris og UTC-periode.

Foerste version anvender samme doegnplan alle ugens dage for almindelige
C-kunder. DataHub viser faktisk forskellige hverdags-/weekendperioder for
nogle andre Cerius-kundegrupper; disse grupper er endnu ikke understoettet.
