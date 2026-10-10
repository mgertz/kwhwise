# Release-processen

Projektet bruger Conventional Commits og semantic-release. Go-programmet og
Docker-imaget har ikke brug for Node; Node 24/npm bruges kun til release-tooling
og tests af release-overleveringen i GitHub Actions.

## Pull requests

`.github/workflows/pr-tests.yaml` koerer ved oprettelse, opdatering,
genaabning og redigering af PR'er, ogsaa mod integration branches.

1. **Verify PR Title** kontrollerer Conventional Commit-formatet.
2. **Go Test & Build** kontrollerer formatering, koerer race-tests og `go vet`,
   bygger Linux-binaerer til amd64/arm64 og tester release-helperen.

Eksempler paa PR-titler:

```text
feat: add electricity price calculation
fix(mqtt): deliver state to late subscribers
fix(tariffs): update national rates for 2027
docs: explain grid profiles
ci: update the release workflow
```

Titlen skal beskrive aendringen, ikke blot hedde `Release 1.0`.
Ved squash merge boer PR-titlen bruges som commit-besked. Semantic-release
analyserer de **faktiske commits paa main**, ikke PR-titlen direkte.

Ved promotion af en integration branch med flere grupperede commits, som
`release/1.0`, bruges merge commit eller rebase merge for at bevare grupperne i
release-noterne. Squash merge af hele branchen samler dem til en enkelt commit.

De centrale checks til branch protection er **Verify PR Title** og
**Go Test & Build**. De skal begge vaere bestaaet foer merge.

## Release efter merge

`.github/workflows/prod-release.yaml` koerer paa push til `main`, normalt
efter en PR er merged. Integration branches som `release/1.0` publicerer ikke
releases.

```text
Go Test & Build
  -> Semantic Release
  -> Build and Push Docker Image (kun ved en ny release)
```

Release-jobbet henter hele Git-historikken og alle tags. `.releaserc.json`
bruger samme Conventional Commits-preset til commit-analyse og release notes,
og GitHub-pluginet opretter GitHub-releasen. Der publiceres ikke en npm-pakke,
og package.json-versionen er ikke applikationens versionsnummer.

`scripts/semantic-release.mjs` bruger semantic-release's API-resultat:

- `false` betyder ingen ny release, og Docker-jobbet springes over.
- En ny release giver versionsnummer og det praecise Git-tag via job-outputs.
- En release-fejl fejler jobbet, saa Docker ikke publicerer et gaettet tag.

Docker-jobbet checker det publicerede Git-tag ud, bygger til
`linux/amd64` og `linux/arm64`, og pusher til GitHub Container Registry.
Eksempel ved version 1.2.3:

```text
ghcr.io/mgertz/kwhwise:1.2.3
ghcr.io/mgertz/kwhwise:1.2
ghcr.io/mgertz/kwhwise:1
ghcr.io/mgertz/kwhwise:latest
```

`APP_VERSION=1.2.3` indbygges i Go-binaeren og vises i loggen og Home Assistant
device-info. Lokale builds bruger `dev`, medmindre versionen angives eksplicit.

Versionsnummeret kan kontrolleres uden broker eller prisopslag:

```bash
docker run --rm ghcr.io/mgertz/kwhwise:1.2.3 --version
```

Release-workflowen afvikles sekventielt uden at afbryde en igangvaerende
release, saa et oprettet tag ikke efterlades midt i Docker-publiceringen, og
parallelle runs ikke overskriver `latest` i forkert raekkefoelge.

## Versionsregler

| Commit | Resultat efter foerste release |
|---|---|
| `fix: ...` | Patch, fx 1.0.0 -> 1.0.1 |
| `perf: ...` | Patch |
| `feat: ...` | Minor, fx 1.0.0 -> 1.1.0 |
| `feat!: ...` eller `BREAKING CHANGE:` | Major, fx 1.x -> 2.0.0 |
| `docs: ...`, `ci: ...`, `chore: ...` | Normalt ingen release |

Foerste relevante release bliver **1.0.0**. For den foerste samlede
implementation kan PR'en eksempelvis hedde:

```text
feat: add the kwhwise electricity price calculator
```

Tarifaendringer paavirker den pris, programmet beregner, og skal derfor
typisk vaere `fix(tariffs): ...`, saa en ny container med dataene publiceres.

## GitHub-adgang

Workflows bruger kun repositoryets indbyggede `GITHUB_TOKEN`. Release-jobbet
har adgang til tags/releases og release-kommentarer; Docker-jobbet har
`packages: write`. PR-kontrollerne har kun laeseadgang.

Efter foerste GHCR-publicering skal pakkens visibility saettes til **public**,
hvis brugerne skal kunne hente den uden login. Selve projektet er MIT-licenseret.

## Lokal verifikation

```bash
npm ci --ignore-scripts
npm run test:release
go test -race ./...
go vet ./...
docker build --build-arg APP_VERSION=1.0.0 -t kwhwise:release-check .
```

Release-helperens tests bruger syntetiske release-resultater og opretter
ingen Git-tags, GitHub-releases eller Docker-publiceringer. De kontrollerer
ogsaa de rigtige Conventional Commit-release-regler.

Et rigtigt `npm run release` er beregnet til workflowen paa `main` og kraever
`GITHUB_OUTPUT`, saa almindelig lokal udvikling ikke udloeser en release.

Release-vaerktoejernes versioner er fastlaast i package-lock.json. Den aktuelle
npm-audit rapporterer upstream-fund i blandt andet `braces` og npm's bundtede
afhaengigheder. `braces` har aktuelt ingen rettet npm-release. Disse pakker
findes ikke i Go-binaeren eller runtime-imaget; nye rettelser skal vurderes ved
opdatering af CI-toolingen frem for en tvungen nedgradering til aeldre plugins.
