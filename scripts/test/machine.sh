#!/usr/bin/env bash
# A fake complete machine for the installer tests (needs lib.sh sourced first).

# complete_machine: stubs and fake project files for a machine with everything.
complete_machine() {
    stub git 'echo "git version 2.43.0"'
    stub cmake 'echo "cmake version 3.28.3"'
    stub c++ 'echo "c++ (GCC) 13.2.0"'; stub cc 'echo "cc (GCC) 13.2.0"'
    stub g++ 'echo "g++ (GCC) 13.2.0"'; stub gcc 'echo "gcc (GCC) 13.2.0"'
    stub make 'echo "GNU Make 4.3"'
    stub curl 'echo "curl 8.5.0"'
    stub ffmpeg 'case "$*" in *-encoders*) echo " V....D libx264              H.264";; *) echo "ffmpeg version 6.1.1";; esac'; stub ffprobe 'echo "ffprobe version 6.1.1"'
    stub go 'echo "go version go1.22.3 linux/amd64"'
    stub node 'echo "v20.11.1"'; stub npm 'echo "10.2.4"'
    stub nvidia-smi 'echo "0, NVIDIA GeForce RTX 4070, 8.9, 12282"; echo "1, NVIDIA GeForce RTX 5060 Ti, 12.0, 16311"'
    stub nvcc 'echo "Cuda compilation tools, release 12.8, V12.8.61"'
    stub google-chrome-stable 'echo "Google Chrome 126.0.6478.55"'
    stub blender 'echo "Blender 5.2.3 LTS"; echo "	build date: 2026-10-01"'
    stub ollama 'case "$1" in --version) echo "ollama version is 0.5.0";; list) echo "NAME ID"; echo "qwen3:8b abc";; esac'
    fake_repo_file bin/sd '#!/usr/bin/env bash
echo "stable-diffusion.cpp version master-948-228c707"'
    fake_repo_file bin/piper '#!/usr/bin/env bash
echo "usage: piper [options]"'
    fake_repo_file bin/ace-lm '#!/usr/bin/env bash
echo ace-lm'; fake_repo_file bin/ace-synth '#!/usr/bin/env bash
echo ace-synth'
    fake_repo_file bin/yt-dlp '#!/usr/bin/env bash
echo 2026.08.19'
    fake_repo_file third_party/gsap/gsap.min.js '/*!
 * GSAP 3.13.0
 */'
    fake_repo_file hyperframes/blocks/x/block.json '{}'
    fake_repo_file third_party/hyperframes-runtime/hyperframe.runtime.iife.js 'x'
    fake_repo_file internal/music/hyperframes/libs.txt '# libs
0000000000000000000000000000000000000000000000000000000000000000  https://h/a.js'
    fake_repo_file third_party/hyperframes-libs/h/a.js 'x'
    fake_repo_file bin/realesrgan/realesrgan-ncnn-vulkan '#!/usr/bin/env bash
echo "Usage: realesrgan-ncnn-vulkan -i infile -o outfile"'; fake_repo_file bin/realesrgan/models/realesr-animevideov3-x2.bin 'x'
    fake_repo_file bin/rife/rife-ncnn-vulkan '#!/usr/bin/env bash
echo "Usage: rife-ncnn-vulkan -0 infile0 -1 infile1 -o outfile"'; fake_repo_file bin/rife/rife-v4.6/flownet.bin 'x'
    fake_repo_file bin/veritmo '#!/usr/bin/env bash
echo "Usage of veritmo"'
    fake_repo_file voices/v.onnx; fake_repo_file voices/v.onnx.json '{}'
    fake_repo_file models/m.safetensors
}

