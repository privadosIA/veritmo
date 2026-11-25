# Guía de Veritmo: conceptos, terminal y un ejemplo completo

[← Volver al README](../README.md) · [English version](TUTORIAL.md)

La guía pantalla a pantalla del **dashboard** (entorno, modelos, el editor, runs, historial, estadísticas, música e imágenes propias) vive con el dashboard, en el proyecto `dashboards-lab` (`docs/TUTORIAL.es.md`), porque describe pantallas que cambian con él. Este documento conserva lo que no depende del front end: los conceptos, qué hacer cuando algo falla, lo mismo desde la terminal, el glosario, la pantalla de API del servidor y un ejemplo completo con su `scenes.yaml`. La numeración es la de la guía completa, así que las secciones que faltan aquí (de la 3 a la 10) están en la del dashboard.

## Índice

- [1. Qué hace esto, en una frase](#1-qué-hace-esto-en-una-frase)
- [2. Conceptos mínimos](#2-conceptos-mínimos)
- [11. Cuando algo falla](#11-cuando-algo-falla)
- [12. Misma cosa desde la terminal](#12-misma-cosa-desde-la-terminal)
- [13. Glosario](#13-glosario)
- [La pantalla «API»](#la-pantalla-api)
- [Un ejemplo completo: dos amigas en un banco que se van patinando](#un-ejemplo-completo-dos-amigas-en-un-banco-que-se-van-patinando)

---

## 1. Qué hace esto, en una frase

Escribes **escenas** (un texto para generar una imagen y, si quieres, un texto para que alguien lo narre) y Veritmo produce un **vídeo narrado**, o solo las **imágenes**, o —aparte, sin relación con el vídeo— **canciones** a partir de un estilo y una letra (ver Música). Si ya tienes la imagen, puedes usarla tal cual en una escena, girarla, o partir de ella para generar otra, y ponerle música de fondo al vídeo. Todo se ejecuta en tu máquina, con tus GPUs; no manda nada a ningún servicio.

Por dentro, cada escena pasa por estas fases, y el dashboard te las enseña una a una mientras se ejecutan:

| Fase | Qué pasa | Programa que lo hace |
|---|---|---|
| `tts` | Convierte el texto de narración en audio | Piper |
| `probe` | Mide cuánto dura ese audio | ffprobe |
| `render` | Genera la imagen de la escena a partir del prompt | `sd` (stable-diffusion.cpp) |
| `animate` | *(opcional)* Convierte la imagen en un clip con movimiento | `sd` con Wan 2.2 |
| `clip` | Une imagen (o animación) y audio en un trozo de vídeo | ffmpeg |
| `concat` | Pega todos los trozos en el vídeo final | ffmpeg |
| `bgm`, `mix` | *(opcionales)* Generan la música de fondo y la mezclan bajo la voz | motor de música y ffmpeg |

Dos cosas que te ahorran tiempo y que conviene saber desde el principio:

- **Caché**: cada fase de cada escena se guarda según su contenido. Si cambias solo el texto de una escena, solo se rehace lo que depende de ese cambio. Esto es lo que enseña la tabla «Caché» de la pantalla Runs.
- **Nada se borra solo**: ni modelos ni vídeos. Borrar el historial de runs no borra los vídeos.

## 2. Conceptos mínimos

- **Proyecto**: una carpeta con su `scenes.yaml` (el guion), su caché (`tmp/`) y sus resultados (`out/`). El dashboard crea y edita ese archivo por ti.
- **Escena**: un fotograma-idea: prompt de imagen + narración + (opcional) movimiento.
- **Modelo**: el «cerebro» que dibuja. Hay dos formas de configurarlo:
  - **Checkpoint único**: un solo archivo (por ejemplo Stable Diffusion 1.5 o XL). Lo más sencillo.
  - **Conjunto de modelos**: tres archivos que trabajan juntos (modelo de difusión + codificador de texto + VAE). Lo usan Krea 2 e IntoRealism, que dan imágenes mucho más realistas.
- **Prompt**: la descripción de la imagen, en inglés funciona mejor con casi todos los modelos.
- **Run**: una ejecución del pipeline sobre un proyecto. Queda en el historial.
- **GPU**: la tarjeta gráfica que hará el trabajo de `sd`. Si tienes dos, puedes elegir una o las dos.

## 11. Cuando algo falla

| Síntoma | Qué mirar |
|---|---|
| Un binario sale como «falta» en Entorno | Ejecuta el comando que se indica debajo (`bash scripts/get-piper.sh`, etc.) y pulsa **Actualizar**. Si lo has instalado pero sigue sin verse, el servidor no lo encuentra en su `PATH`: arráncalo con `PATH=$PWD/bin:$PATH`. |
| Una escena con calidad propia sale con otro tamaño que el resto | Es lo esperado: el vídeo final mantiene la resolución del proyecto y esa escena se recorta hasta ella (sin estirarla). Para cambiar el tamaño de todo el vídeo, cambia la resolución en Defaults. |
| En el mezclador, «Oír sin mezclar» no suena igual que la mezcla exportada | Es una aproximación en el navegador (volumen hasta 2 solo si el audio es del mismo origen que la página). Pulsa **Mezclar** para generar el archivo y oír el resultado real. |
| Falla en `render` y se queda sin memoria | Mira «Recursos» del run: si la VRAM llega al 100 %, baja la resolución o usa otro modelo. |
| El run se cuelga o el equipo se queda sin RAM | Los modelos grandes (conjuntos, Wan) copian el modelo a RAM. `./serve.sh` ya limita la memoria al 85 % de la RAM por defecto (unos 52G en 64 GB, sin swap): si un run se pasa, se corta ese proceso en vez de colgar el equipo. Cámbialo con `MEM_MAX=40G ./serve.sh`. Si lanzas `bin/veritmo serve` a mano: `systemd-run --user --scope -p MemoryMax=24G -p MemorySwapMax=0 bin/veritmo serve …`. |
| La imagen sale mal con Krea 2 / IntoRealism | Comprueba que son 8 pasos y cfg 1: usa **Usar ajustes recomendados**. |
| Una escena se rehace entera aunque no la tocaste | Comprueba que **keep-tmp** está activado; sin él no hay caché entre runs. |

## 12. Misma cosa desde la terminal

Todo lo que hace el dashboard se puede hacer sin él, con el mismo `scenes.yaml`:

```bash
bin/veritmo --gpu 1 --keep-tmp projects/mi-proyecto/scenes.yaml
```

Banderas útiles (van **antes** del archivo): `--gpu 0,1`, `--keep-tmp`, `--force`, `--only <id-escena>`, `--mode images`, `--vram-warn-mb 2048`. Para convertir un dataset de canciones: `bin/veritmo import --mode music -o scenes.yaml canciones.json` (las banderas, también aquí, antes del fichero).

Para usar el dashboard desde otra máquina, protege el acceso con `VERITMO_TOKEN` y haz un túnel SSH (`ssh -L 4666:127.0.0.1:4666 usuario@servidor`). Los detalles y todos los campos del `scenes.yaml` están en [SCENES_YAML.md](SCENES_YAML.md); los endpoints, en [`docs/http-api.md`](http-api.md).

Para que la IA ayude a escribir prompts desde la terminal:

```bash
bin/veritmo assist image "una cafetería por la mañana"
bin/veritmo assist motion "ella pasa una página"
```

(Necesita Ollama en marcha; trabaja en CPU para no quitar VRAM al render.)

Para sacar el prompt de una imagen o un vídeo que ya tienes, y para usar otro proveedor en una ejecución:

```bash
bin/veritmo describe foto.png                 # imprime el prompt que la dibujaría
bin/veritmo describe --split clip.mp4         # una línea por plano: [inicio-fin] prompt, y su MOTION:
bin/veritmo describe --provider nube foto.png
bin/veritmo assist --provider lan image "una cafetería por la mañana"
```

`describe` necesita un modelo de visión en Ollama (`ollama pull gemma4:26b`). `--provider` recibe el id de un proveedor registrado en **Configuración**; uno remoto tiene que haberse aceptado antes allí (la terminal no puede aceptarlo), y si no, los comandos siguen el enrutado elegido en la tarjeta **Tareas**.

## 13. Glosario

- **VRAM**: memoria de la tarjeta gráfica. Si no cabe el modelo, falla.
- **RAM**: memoria del ordenador. Algunos modelos pasan por ella antes de ir a la GPU.
- **Checkpoint / modelo**: archivo con lo aprendido por la IA (`.safetensors` o `.gguf`).
- **Cuantizado (Q4, fp8, int8)**: versión comprimida del modelo. Ocupa menos, pierde algo de calidad; `Q4` y `fp8` van bien aquí.
- **LoRA**: ajuste pequeño que cambia el estilo o el sujeto de un modelo.
- **VAE / codificador de texto**: piezas auxiliares de los conjuntos de modelos.
- **Steps (pasos)**: veces que la IA retoca la imagen. Más pasos, más lento.
- **cfg_scale**: cuánto obedece al prompt. Cada modelo tiene su valor (los Turbo, 1).
- **Seed (semilla)**: número que fija el azar. Misma semilla + mismo prompt = misma imagen. Vacío = aleatoria en cada run.
- **Sampler**: algoritmo de dibujado (`euler` va bien casi siempre).
- **Wan**: modelo que anima una imagen para convertirla en un clip.
- **TTS**: texto a voz (Piper).
- **Caché**: lo ya hecho y guardado para no repetirlo.
- **Run**: una ejecución del pipeline.
- **img2img**: generar una imagen partiendo de otra tuya en lugar de desde ruido; la **fuerza** dice cuánto puede alejarse.
- **Remix / referencia**: un remix reinterpreta una canción que ya existe; una referencia solo orienta el sonido de una canción nueva.
- **Estilo / calidad (nivel)**: el estilo (fotorrealismo, anime, general) elige el modelo instalado más adecuado; la calidad (Baja, Media, Alta) elige resolución, pasos y, en vídeo, el Wan. Valen para el proyecto entero o, desde «Calidad de esta escena», para una sola escena. No se guardan como tal: solo los valores que escriben.
- **Mezclador / «Oír sin mezclar»**: la ventana donde se colocan varias canciones y audios en una línea de tiempo; «Oír sin mezclar» los toca juntos en el navegador, con los cambios al instante, sin generar el archivo (eso lo hace «Mezclar»).
- **Favorita**: una canción marcada con estrella. Se guarda con el proyecto, identificada por pista y semilla.
- **Nivel de calidad (música)**: Rápida, Normal o Máxima: un modelo de síntesis y sus pasos elegidos de una vez.
- **LUFS**: la sonoridad de una grabación tal como se oye; −14 LUFS es el objetivo habitual de las plataformas de música. *Normalizar* en el mezclador apunta a él.
- **Música de fondo**: banda sonora instrumental que se mezcla bajo la narración de un vídeo, bajando sola cuando habla la voz.
- **Referencia / describir**: una imagen o un vídeo que subes solo para que un modelo de visión escriba su prompt; la escena nueva se genera a partir de ese texto, nunca del archivo.
- **Modelo de visión**: un modelo que sabe leer imágenes (en Ollama, por ejemplo `gemma4:26b`); describe las referencias y juzga los fotogramas de la revisión de calidad.
- **Proveedor**: dónde se ejecuta un modelo: el Ollama local integrado, otro Ollama o cualquier servidor compatible con OpenAI (OpenAI, OpenRouter, Together, Groq, vLLM, LM Studio…). Los registras en Configuración.
- **Clave de API**: el secreto que pide un proveedor en la nube. Se guarda en el servidor, es de solo escritura (no se vuelve a mostrar) y nunca se envía por http plano a otra máquina.
- **Tarea (enrutado)**: una de las cinco cosas que usan un modelo: el asistente de prompts, describir una imagen, describir un vídeo, la revisión de clips y escribir la página de un gráfico animado. Cada una corre en el proveedor que elijas en la tarjeta **Tareas**; todas empiezan en el Ollama local.
- **Consentimiento**: tu aceptación explícita de que una tarea enviará imágenes, vídeos o textos a un proveedor que no es este equipo. Va atado a la dirección del proveedor, se ve como una insignia «☁ externo» y nada cambia de proveedor por sí solo.
- **Plano**: una toma continua de un vídeo; describir un vídeo «una escena por plano» hace una escena por cada uno.

## La pantalla «API»

Si quieres saber qué ofrece el servidor (o probar una petición a mano), abre **API** en el menú, justo debajo de Entorno. Es la referencia de todos los endpoints, con sus parámetros y respuestas:

![API](tutorial/17-api-swagger.png)

- Pulsa un endpoint para desplegarlo, y **Try it out** para probarlo. La petición va al servidor al que estás conectado, con el token de «Conexión»: no hay que pegarlo otra vez.
- Ojo: **actúa sobre el servidor real**. Lo que crees o borres desde ahí queda hecho.
- Si sale un aviso en vez de la lista, normalmente es que falta el token en «Conexión».
- La referencia usa el aspecto estándar de Swagger, distinto al del resto del dashboard.

## Un ejemplo completo: dos amigas en un banco que se van patinando

Un caso real, con su `scenes.yaml` (es el de `projects/chicas-patines`): un clip de unos 30 segundos en el que dos chicas en bikini están sentadas en un banco del parque hablando, y luego se levantan y se van patinando. Enseña cómo pensar un **vídeo** cuyo sujeto se mueve, y lo que medimos.

### Qué no funciona, y por qué

Pedirlo como una sola escena («se sientan hablando y luego se levantan y patinan») falla por cuatro razones que salen de cómo funciona el pipeline:

1. **Una escena es una imagen animada durante 2 a 4 segundos**, no un clip continuo. Un vídeo de 30 segundos necesita unas **15 escenas** con cortes entre ellas.
2. **La animación va hacia delante y luego marcha atrás.** Se guarda hacia delante e invertida para que haga bucle sin saltos; si la escena dura más que la parte hacia delante, empieza la mitad invertida y la acción **se deshace sola** (se levantan y se vuelven a sentar). Por eso la escena dura lo que la parte hacia delante: fotogramas ÷ fps = 33 ÷ 16 = 2,06 s, de ahí `silence_seconds: 2`.
3. **Una sola acción por escena.** Wan hace bien un gesto (hablar, reír, girar la cabeza) y mal una secuencia.
4. **Los clips no se enlazan**: cada escena parte de una imagen nueva, así que las dos chicas cambian de cara entre planos si no las describes igual cada vez. Para eso sirve `defaults.subject_prompt`: se antepone a **todos** los planos (los personajes y el lugar), y cada `prompt` solo describe el plano.

### El proyecto

`silence_seconds` hace la escena muda y con esa duración exacta (no hace falta narración ni voz; si quieres música, `bgm`). Las imágenes usan el conjunto de modelos probado en este proyecto (IntoRealism + Qwen + `wan_2.1_vae`, 1024x576, 8 pasos) y Wan 2.2 5B anima 33 fotogramas por plano:

```yaml
# Clip de ~30 s: dos chicas en bikini en un banco del parque que se levantan y se van patinando.
# 15 planos de 2 s (el de levantarse, 1,5 s): unos 29,5 s en total.
# Cada plano es una imagen que Wan anima hacia delante (33 fotogramas a 16 fps = 2,06 s): la escena
# dura 2 s para no llegar nunca a la marcha atrás del bucle. Medido con Wan 5B: el movimiento sale
# bien durante ~1,5-2 s y después las piernas y los cuerpos se deforman; por eso el plano de
# levantarse dura 1,5 s y se corta a otro plano ya de pie.
# subject_prompt describe a las dos chicas y el lugar y se antepone a todos los planos, para que
# sean las mismas en cada corte. Cada `prompt` solo describe el plano; `motion_prompt`, solo lo que se mueve.
project: chicas-patines
output:
  mode: video
  file: out/chicas-patines.mp4
defaults:
  voice: voices/en_US-lessac-medium.onnx   # no se usa (todas las escenas son mudas), pero se exige
  resolution: 1024x576
  sampler: euler
  steps: 8
  fps: 24
  model_set:
    diffusion_model: models/intorealismKrea2_v40.safetensors
    llm: models/Qwen3VL-4B-Instruct-Q4_K_M.gguf
    vae: models/wan_2.1_vae.safetensors
    cfg_scale: 1
  subject_prompt: >-
    photo of a brunette young woman with long wavy hair in a red bikini and a blonde young woman
    with a short bob in a blue bikini, both wearing white roller skates, sunny city park, wooden bench,
    green trees, soft golden light, 35mm, natural skin,
  negative_prompt: blurry, low quality, deformed hands, extra fingers, extra limbs, bad anatomy, text, watermark, bent over, rear view close-up, from behind, buttocks
  animate:
    diffusion_model: models/video/Wan2.2-TI2V-5B-Q8_0.gguf
    t5xxl: models/video/umt5-xxl-encoder-Q8_0.gguf
    vae: models/video/wan2.2_vae.safetensors
    frames: 33          # 2,06 s hacia delante (con 49, 3 s, se deformaba en las pruebas)
    steps: 20
scenes:
  - id: s01_bench_wide
    seed: 101
    silence_seconds: 2
    animate: true
    prompt: both sitting on the park bench talking and smiling, wide shot
    motion_prompt: the two women chat, the brunette gestures with one hand while talking, the blonde nods and laughs, leaves move gently in the breeze, static camera

  - id: s02_brunette_talking
    seed: 102
    silence_seconds: 2
    animate: true
    prompt: medium close-up of the brunette sitting on the park bench, looking at her friend
    motion_prompt: she talks, lips moving, smiles and tilts her head, hair moves slightly in the wind, static camera

  - id: s03_blonde_laughing
    seed: 103
    silence_seconds: 2
    animate: true
    prompt: medium close-up of the blonde sitting on the park bench
    motion_prompt: she laughs, tucks her hair behind her ear, glances to the side, static camera

  - id: s04_look_off
    seed: 104
    silence_seconds: 2
    animate: true
    prompt: both sitting on the park bench, two-shot
    motion_prompt: both turn their heads towards the path, then look at each other with excitement, static camera

  - id: s04b_hands_talking
    seed: 111
    silence_seconds: 2
    animate: true
    prompt: close-up of the brunette's hands gesturing while she talks, both sitting on the park bench, shallow depth of field
    motion_prompt: the hands move expressively while she talks, the blonde listens and smiles in the background, static camera

  - id: s04c_two_smile
    seed: 112
    silence_seconds: 2
    animate: true
    prompt: both sitting close together on the park bench smiling, medium shot
    motion_prompt: they smile and glance at each other, one nudges the other playfully, static camera

  - id: s05_skates_detail
    seed: 105
    silence_seconds: 2
    animate: true
    prompt: close-up of two pairs of legs on the park bench, white roller skates with colourful wheels resting on the ground
    motion_prompt: their feet tap the ground impatiently, wheels roll slightly, static camera

  - id: s06_stand_up
    seed: 106
    silence_seconds: 1.5
    animate: true
    prompt: medium shot from the front, both sitting on the edge of the park bench about to stand up, hands on the bench seat, smiling
    motion_prompt: both push off the bench with their hands and stand up smiling, balancing on their roller skates, static camera

  - id: s07_roll_away_back
    seed: 107
    silence_seconds: 2
    animate: true
    prompt: wide landscape shot, the two women are small in the frame, full body, seen from the side in profile facing right, standing on roller skates on a tree-lined park path, bench in the background
    motion_prompt: the two women start skating along the path side by side, gliding away from the bench, hair flowing, static camera

  - id: s08_skating_to_camera
    seed: 108
    silence_seconds: 2
    animate: true
    prompt: medium shot skating towards the camera on a park path, smiling
    motion_prompt: they skate towards the camera smoothly, the brunette waves, hair flowing, camera slowly moves back

  - id: s09_side_tracking
    seed: 109
    silence_seconds: 2
    animate: true
    prompt: side view skating along a tree-lined path, laughing
    motion_prompt: they glide along the path laughing, trees pass in the background, camera tracks with them

  - id: s11_laughing_closeup
    seed: 113
    silence_seconds: 2
    animate: true
    prompt: close-up of both laughing while skating along the park path, front view, trees blurred in the background
    motion_prompt: they laugh and glide forward, hair flowing in the wind, camera moves backward keeping them in frame

  - id: s12_wave
    seed: 114
    silence_seconds: 2
    animate: true
    prompt: medium shot from the front, both skating on a sunny park path, one waving at the camera
    motion_prompt: the brunette waves at the camera while skating, the blonde laughs, camera tracks backward

  - id: s13_curve_path
    seed: 115
    silence_seconds: 2
    animate: true
    prompt: wide shot, both skating side by side along a curved tree-lined path, side view
    motion_prompt: they skate along the curve side by side, gliding smoothly, static camera

  - id: s10_off_into_distance
    seed: 110
    silence_seconds: 2
    animate: true
    prompt: wide shot skating down a long park path towards the sun
    motion_prompt: they skate away into the distance, sunlight glows through the trees, static camera
```

### Cómo se probó (para que lo hagas igual)

No lances los 15 planos para averiguarlo. Prueba primero los difíciles, **primero la imagen y luego el movimiento**:

1. **La imagen**, en modo imágenes, unos 17 s: `veritmo --mode images --only s06_stand_up scenes.yaml`. Mírala antes de animar: el primer intento del plano de levantarse (*«low angle, about to stand up»*) salió como una vista trasera inclinada, así que el prompt pasó a un plano frontal sentadas en el borde del banco y el `negative_prompt` ganó *bent over, from behind, rear view close-up*. El plano de patinar también pasó de vista trasera a **plano general de perfil**.
2. **El movimiento**, unos 85 s: `veritmo --keep-tmp --gpu 1 --only s06_stand_up scenes.yaml`. Luego mira fotogramas del clip. La primera prueba usó 49 fotogramas (3 s) y el clip se miró fotograma a fotograma (segundos 0 a 2,8).

   El primer segundo y medio estaba bien (se inclina hacia delante y se impulsa con las manos); después las piernas se agitan y cae por encima del banco. Le pasó lo mismo al plano de patinar: se aleja bien y luego los cuerpos se doblan. Por eso los planos se recortaron a **33 fotogramas (2 s)**, y el de levantarse a 1,5 s.

   Con 33 fotogramas los dos se mantienen coherentes: se levantan (algo bruscos: el movimiento se comprime en 1,5 s) y se deslizan mientras la cámara las sigue. El corte al plano siguiente esconde el resto.
3. **Corrige lo que no funcione** cambiando el `prompt` (imagen) o solo el `motion_prompt` (movimiento: la imagen no se vuelve a dibujar). La caché también te dice qué va a rehacer.
4. **Luego todo**: `veritmo --keep-tmp --gpu 1 scenes.yaml` (o *Crear* en el editor). Con el 5B cada plano tarda unos 100 s (17 s la imagen, 85 s el movimiento): los 15 planos, unos 25 minutos. En una máquina de 30 GB lánzalo con un tope de memoria, como dice la sección de Animación.

> **`--only` y la caché**: hasta que se corrigió un fallo, `--only` también escribía las entradas de caché de las escenas que dejaba fuera, así que un run posterior podía creer que ya estaban hechas y fallar al unir el vídeo (*Error opening input … scene_….mp4*). Está corregido; si alguna vez ves ese error en un proyecto en el que ejecutaste las escenas de una en una, borra el `tmp/manifest.json` del proyecto y vuelve a lanzarlo.

### Cómo escribir los prompts

- **`prompt` describe la imagen, con el plano primero**: *«medium close-up of the brunette sitting on the park bench»*, *«wide landscape shot … seen from the side in profile»*. Los personajes y el lugar ya están en `subject_prompt`.
- **`motion_prompt` describe solo lo que se mueve**, en presente, con una acción principal y la cámara: *«she talks, lips moving, smiles and tilts her head, hair moves slightly in the wind, static camera»*. Para «hablando»: la boca se mueve de forma genérica (no hay sincronización de labios), así que cubre el diálogo con música o una locución.
- **Di la cámara**: `static camera`, `camera slowly moves back`, `camera tracks with them`. Si no, puede derivar.
- **Corta en la acción**: acaba un plano justo cuando empieza el movimiento (el de levantarse) y empieza el siguiente con la acción ya en marcha (patinando). El corte esconde lo que el modelo no sabe hacer.
- **Elige encuadres que aguanten el movimiento**: las vistas traseras o muy bajas se deforman con facilidad (y la primera imagen de este ejemplo salió mal compuesta justo así); un plano frontal, de perfil o general esconde más un cuerpo que se dobla.

### Rótulos, créditos y gráficos animados

El texto es lo que peor dibujan los modelos de difusión y lo que mejor dibuja un navegador. Por eso hay dos formas de poner texto en pantalla, las dos escritas por el modelo y dibujadas con **Chrome o Chromium** (no hay nada que descargar: la pantalla Entorno muestra cuál se usa, y Configuración > Navegador deja elegir otro):

1. **Un rótulo sobre una escena.** En cualquier escena, hacia el final del formulario, *Añadir un rótulo*. Escribe el texto como lo quieres, o descríbelo («título grande con el nombre y una línea fina debajo»), elige **Arriba, Centro o Abajo** y, si quieres, **Desde** y **Hasta** en segundos (vacío es toda la escena). El rótulo se anima sobre la imagen, la animación o el clip de la escena, y la imagen se sigue viendo alrededor.
2. **Una escena que es un gráfico animado.** En la escena, junto a *Generar con IA*, *Mi imagen* y *Mi clip*, elige **Gráfico animado** y di qué muestra («tres ideas que aparecen una a una sobre azul oscuro»). Toda la escena es una página animada tan larga como su narración: listas, esquemas, gráficos, títulos, créditos. Un proyecto hecho solo de estas escenas no necesita GPU. Para empezar uno, en la pantalla Proyectos elige **Proyecto de gráficos animados**: crea un vídeo con tres páginas mudas (un título, una lista y los créditos) unidas con un fundido corto, listo para editar, con *Escribir la página* o con un run.

La página se escribe **una vez**, la primera vez que un run la necesita (o al pulsar *Escribir la página*), y se guarda en el proyecto: después se reutiliza la misma, así que el resultado no cambia entre runs y no se vuelve a llamar al modelo. La **vista previa** la reproduce en el editor con un control de tiempo, sin dibujar ningún vídeo; *Editar la página* deja cambiar su código a mano y *Escribir otra página* pide una nueva. Lo que dice cómo se hizo («escrita por qwen3:8b · Chrome 126», «luego editada») es también lo que muestran los detalles del clip en el selector de clips. El modelo que la escribe se elige en Configuración > *Modelo de los gráficos animados* (vacío usa `gemma4:26b`, o `gemma4:31b`, si están instalados y, si no, el del asistente de prompts; `gemma4` escribe páginas mucho mejores que un modelo pequeño) y su proveedor en Configuración > Tareas (*Gráficos animados y rótulos*). Como los demás ayudantes de IA, los controles que piden una página al modelo (la opción *Gráfico animado*, añadir un rótulo, el botón *Proyecto de gráficos animados* y *Escribir la página*) **no se muestran** cuando ningún modelo puede escribirla, y un aviso dice qué instalar (`ollama pull gemma4:26b`); las páginas ya guardadas siguen reproduciéndose, se editan a mano y se dibujan en un run sin ningún modelo. Si falta el navegador, los botones quedan desactivados y dicen por qué.

**La línea de tiempo.** Las páginas que se escriben ahora declaran sus entradas como una lista de *pistas* (qué se mueve, cuándo empieza, cuánto dura y con qué curva) en lugar de CSS. Cuando una página las tiene, el editor muestra debajo de la vista previa una **Línea de tiempo**: una fila por pista con una barra sobre el eje de tiempo de la escena. Arrastra una barra para moverla o su borde derecho para cambiar la duración (de 0,05 s en 0,05 s); elige una fila para cambiar por número su inicio, duración, escalonado, curva y valores. La vista previa cambia al instante, **Deshacer** y **Rehacer** recorren tus cambios y *Guardar los cambios* guarda la página para que el vídeo la use. No necesita ningún modelo, así que sigue funcionando cuando los controles de IA están ocultos. Una página escrita antes anima con CSS y no tiene pistas: el editor lo dice, y *Escribir otra página* crea una que sí las tenga.

El vídeo se dibuja fotograma a fotograma, así que sale exactamente igual cada vez, tarda segundos y sigue la duración de la narración: una narración más larga lo dibuja de nuevo, nada más lo hace. Las transiciones, las voces y la música de fondo funcionan en estas escenas como en cualquier otra. En el fichero: `motion_graphic: ...` y `overlay: {text, position, start, end}` (ver `SCENES_YAML.md`).

### Fundidos entre escenas

Por defecto los planos se unen con cortes. En los ajustes del proyecto, **Fundido entre escenas (s)** (por ejemplo 0,4) hace que cada unión sea un fundido cruzado corto, con el sonido también cruzado, lo que disimula el salto entre dos clips; en cada escena, **Fundido de entrada** lo cambia (0 es un corte, vacío es el del proyecto). El vídeo queda algo más corto, por la suma de los fundidos.

![Fundido entre escenas en los ajustes del proyecto](tutorial/40-editor-fundido-proyecto.png)

1. **Fundido entre escenas (s)** en los ajustes del proyecto: 0 o vacío es un corte seco.
2. **Tipo de transición**: cómo se unen los clips (fundido cruzado por defecto).

![Fundido de entrada de una escena](tutorial/41-editor-fundido-escena.png)

1. **Fundido de entrada** de una escena: vacío usa el del proyecto, 0 es un corte y cualquier otro número es el fundido cruzado hacia esta escena desde la anterior.

Junto al fundido, **Tipo de transición** (en el proyecto y en cada escena, donde *El del proyecto* es lo normal) cambia cómo se unen los clips: fundido cruzado, fundido a negro o a blanco, disolución, cortinillas, deslizamientos, círculo que se abre o se cierra, radial o pixelado. Solo cuenta si el fundido es mayor que 0.

### Cuando una imagen no cabe en la tarjeta

Si la tarjeta gráfica se queda sin memoria al hacer una imagen (una resolución demasiado alta para el modelo), el run **no falla**: la hace de nuevo un cuarto más pequeña, hasta dos veces, y deja un aviso en el run («la tarjeta se quedó sin memoria a 1408x768: la imagen se hizo de nuevo a 1216x640»). La imagen se usa al tamaño con que se hizo (el vídeo la escala) y el manifiesto de imágenes anota ese tamaño. El aviso te dice que bajes la resolución del proyecto para no malgastar el primer intento. Una imagen hecha a partir de una tuya (imagen a imagen) no se reintenta, ni ningún otro tipo de fallo.

### Si quieres más calidad

Wan 2.2 A14B (dos expertos) mantiene más estables las caras y los cuerpos, pero tarda unos 680 s por plano de 33 fotogramas y ~21 GiB de RAM. El modelo Wan es el del proyecto, así que no puedes mezclar los dos en uno: copia el proyecto, pon el A14B en el `defaults.animate` de la copia y deja en ella solo los planos difíciles (el de levantarse y el primero de patinar), 11 minutos cada uno. Luego trae cada clip bueno al proyecto principal con **Mi clip**: elige el clip que conservó la copia (cada run conserva el clip de cada escena en `out/clips/`), que se copia al proyecto, como explica la sección anterior.
