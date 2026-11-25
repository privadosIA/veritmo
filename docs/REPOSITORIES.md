# The family of repositories

[← Back to the README](../README.md)

Veritmo is one repository of nine that are made together. This is the one table that describes them; every README of the family carries it,
the same in all of them (the link of each row goes to the copy of the repository you are reading from, and the row of the repository itself says so).

| Repository | What it is | Licence |
|---|---|---|
| [`veritmo`](https://github.com/privadosIA/veritmo) | The server: the HTTP API and its contract, the pipelines (video, images, music, 3D), the run history and the services it starts. | Apache-2.0 |
| [`dashboards-labs`](https://github.com/privadosIA/dashboards-labs) | The dashboards (Angular) that talk to the server through its OpenAPI contract. | Apache-2.0 |
| [`forms-lib`](https://github.com/privadosIA/forms-lib) | `@core/dynamic-forms`, the Angular library that draws forms and tables from JSON. | Apache-2.0 |
| [`heartmula`](https://github.com/privadosIA/heartmula) | C# (.NET 8 + TorchSharp) port of [heartlib](https://github.com/HeartMuLa/heartlib): the HeartMuLa song generator, run by the server as a service. | Apache-2.0 |
| [`DiffRhythm2`](https://github.com/privadosIA/DiffRhythm2) | C# port of [DiffRhythm2](https://github.com/ASLP-lab/DiffRhythm2): a diffusion model that writes a whole song. | Apache-2.0 |
| [`lion-clap`](https://github.com/privadosIA/lion-clap) | C# port of [LAION-CLAP](https://github.com/LAION-AI/CLAP): audio and text embeddings, used to recognise songs. | Apache-2.0 |
| [`musicgen-core`](https://github.com/privadosIA/musicgen-core) | The TorchSharp building blocks shared by the five ports. | Apache-2.0 |
| [`triposr`](https://github.com/privadosIA/triposr) | C# port of [TripoSR](https://github.com/VAST-AI-Research/TripoSR): one picture in, one 3D model (`.glb`) out. | Apache-2.0 |
| [`stable-fast-3d`](https://github.com/privadosIA/stable-fast-3d) | C# port of [Stable Fast 3D](https://github.com/Stability-AI/stable-fast-3d): one picture in, one textured 3D model (`.glb`) out. | **Stability AI Community License — not Apache-2.0**: free for research and non-commercial use and under US$1M of yearly revenue (register with Stability AI), a commercial licence above |

The licence of a repository covers its own code. The models the ports read keep their own (see [THIRD_PARTY.md](../THIRD_PARTY.md)).
Stable Fast 3D is the one repository that is not Apache-2.0: it is a port of work under the Stability AI Community License, so the port and its weights
are under it too, and Veritmo never builds or installs it without your explicit acceptance ([MODEL_GENERATION.md](MODEL_GENERATION.md#licences-and-what-each-person-does-for-stable-fast-3d)).

## Keeping the tables equal

The table above is the source. In the private copies of the READMEs the links go to the private repositories instead of GitHub, and the row of the repository
you are in says `(this repository)`; nothing else differs. When a repository is added, renamed or changes licence, edit this table, then run
`bash scripts/check-family-table.sh` (it compares the table of each README with this one, links aside, and says which differ) and fix the READMEs it names.
