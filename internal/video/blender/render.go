package blender

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"veritmo/internal/platform/runner"
)

// EnvVar is the environment variable the script reads its settings from, as JSON. The values to inject travel only
// here: they are never part of the script's text, so no text can break out of it.
const EnvVar = "VERITMO_BLENDER"

// DescriptionFile is the name, inside the frames folder, of the resolved description the script reads.
const DescriptionFile = "scene.json"

// script is the whole of what runs inside Blender (passed inline, no file is written). It opens nothing itself: the
// file is the one on the command line, with auto-execution of its scripts disabled.
const script = `import bpy, os, sys, json, math, mathutils

cfg = json.loads(os.environ['VERITMO_BLENDER'])

def die(msg):
    print('VERITMO_ERROR ' + msg, file=sys.stderr)
    sys.exit(1)

scene = bpy.context.scene
r = scene.render
r.engine = 'CYCLES'
r.resolution_x, r.resolution_y, r.resolution_percentage = cfg['width'], cfg['height'], 100
r.film_transparent = False
r.image_settings.file_format = 'PNG'
r.image_settings.color_mode = 'RGB'
scene.cycles.samples = cfg['samples']
scene.cycles.use_denoising = True
scene.cycles.seed = 0
scene.cycles.use_animated_seed = False

def srgb_to_linear(c):
    return c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4

def colour_of(val):
    if isinstance(val, str):
        h = val.lstrip('#')
        if len(h) != 6:
            die('colour %r: use #rrggbb' % val)
        return tuple(srgb_to_linear(int(h[i:i + 2], 16) / 255.0) for i in (0, 2, 4)) + (1.0,)
    if isinstance(val, list) and len(val) in (3, 4):
        v = [float(x) for x in val]
        return tuple(v) + ((1.0,) if len(v) == 3 else ())
    die('a colour is #rrggbb or a list of 3 or 4 numbers, got %r' % (val,))

def set_colour(mat, val):
    col = colour_of(val)
    if mat.use_nodes:
        for n in mat.node_tree.nodes:
            if n.type == 'BSDF_PRINCIPLED':
                n.inputs['Base Color'].default_value = col
                return
    mat.diffuse_color = col

# --- build: a scene description (contract v1) made into a scene. The description is data: nothing in it is evaluated,
# and every name it gives is looked up in the fixed tables below.
EASES = {
    'linear': ('LINEAR', 'AUTO'),
    'inout': ('BEZIER', 'AUTO'),
    'in': ('CUBIC', 'EASE_IN'),
    'out': ('CUBIC', 'EASE_OUT'),
    'back': ('BACK', 'EASE_OUT'),
    'bounce': ('BOUNCE', 'EASE_OUT'),
    'elastic': ('ELASTIC', 'EASE_OUT'),
    'step': ('CONSTANT', 'AUTO'),
}
ALIGNS = {'left': 'LEFT', 'center': 'CENTER', 'right': 'RIGHT'}
LIGHT_KINDS = {'sun': 'SUN', 'area': 'AREA', 'point': 'POINT'}
LIGHT_POWER = {'sun': 2.0, 'area': 500.0, 'point': 500.0}
ADD_MESH = {
    'cube': lambda s: bpy.ops.mesh.primitive_cube_add(size=s),
    'sphere': lambda s: bpy.ops.mesh.primitive_uv_sphere_add(radius=s / 2.0),
    'cylinder': lambda s: bpy.ops.mesh.primitive_cylinder_add(radius=s / 2.0, depth=s),
    'cone': lambda s: bpy.ops.mesh.primitive_cone_add(radius1=s / 2.0, depth=s),
    'torus': lambda s: bpy.ops.mesh.primitive_torus_add(major_radius=s / 2.0, minor_radius=s / 8.0),
    'plane': lambda s: bpy.ops.mesh.primitive_plane_add(size=s),
}

def backdrop_mesh(name, size, curve):
    # A cyclorama: a floor of depth size that curves up (a quarter circle of radius curve, 16 segments) into a wall
    # size / 2 high at +Y, size wide and centred on the origin. A plain mesh with no modifier, so the .blend opens
    # the same anywhere. The profile runs front to back and up; the quad winding makes the normals face the camera.
    half = size / 2.0
    prof = [(-half, 0.0)]
    cy = half - curve
    for i in range(17):
        a = (math.pi / 2.0) * i / 16.0
        prof.append((cy + curve * math.sin(a), curve - curve * math.cos(a)))
    if half > curve:
        prof.append((half, half))
    verts, faces = [], []
    for y, z in prof:
        verts.append((-half, y, z))
        verts.append((half, y, z))
    for i in range(len(prof) - 1):
        a0, b0, a1, b1 = 2 * i, 2 * i + 1, 2 * i + 2, 2 * i + 3
        faces.append((a0, b0, b1, a1))
    me = bpy.data.meshes.new(name)
    me.from_pydata(verts, [], faces)
    me.update()
    for p in me.polygons:
        p.use_smooth = True
    return me

def fcurves_of(idb):
    ad = idb.animation_data
    if ad is None or ad.action is None:
        return []
    act = ad.action
    if hasattr(act, 'fcurves'):
        return list(act.fcurves)
    out = []
    for layer in act.layers:
        for strip in layer.strips:
            cb = strip.channelbag(ad.action_slot)
            if cb is not None:
                out.extend(cb.fcurves)
    return out

def apply_eases(idb, plan):
    # The ease of a key is the way the interval that ENDS at it is travelled; Blender keeps it on the key where the
    # interval starts, so each point gets the ease of the next one. The ease of a channel's first key is not used.
    # step is CONSTANT on that previous point: the value holds until the key's frame and jumps to it there.
    for fc in fcurves_of(idb):
        marks = plan.get(fc.data_path) or plan.get('*')
        if not marks:
            continue
        pts = fc.keyframe_points
        for i in range(len(pts) - 1):
            interp, easing = EASES[marks.get(int(round(pts[i + 1].co[0])), 'inout')]
            pts[i].interpolation = interp
            pts[i].easing = easing
            if interp == 'BEZIER':
                pts[i].handle_left_type = 'AUTO_CLAMPED'
                pts[i].handle_right_type = 'AUTO_CLAMPED'
        fc.update()

def build_scene(path):
    with open(path, 'r', encoding='utf-8') as fh:
        spec = json.load(fh)
    fps = cfg['fps']
    count = cfg['count']

    col = colour_of  # the description arrives resolved: every colour is #rrggbb

    def frame_of(t):
        return min(int(math.floor(t * fps + 0.5)) + 1, count)

    for ob in list(bpy.data.objects):
        bpy.data.objects.remove(ob, do_unlink=True)
    for coll in (bpy.data.meshes, bpy.data.materials, bpy.data.lights, bpy.data.cameras, bpy.data.curves):
        for item in list(coll):
            coll.remove(item)

    scene.render.fps = fps
    scene.render.fps_base = 1.0
    scene.frame_start = 1
    scene.frame_end = count
    scene.view_settings.view_transform = 'Standard'  # the palette's colours stay the palette's colours

    world = scene.world
    if world is None:
        world = bpy.data.worlds.new('world')
        scene.world = world
    if world.node_tree is None:
        world.use_nodes = True
    w = spec.get('world') or {}
    bgn = world.node_tree.nodes['Background']
    bgn.inputs['Color'].default_value = col(w['color'])
    bgn.inputs['Strength'].default_value = float(w.get('strength', 1.0))

    def new_material(name, m):
        mat = bpy.data.materials.new(name)
        if mat.node_tree is None:
            mat.use_nodes = True
        bsdf = mat.node_tree.nodes['Principled BSDF']
        c = col(m['color'])
        bsdf.inputs['Base Color'].default_value = c
        bsdf.inputs['Roughness'].default_value = float(m.get('roughness', 0.5))
        bsdf.inputs['Metallic'].default_value = float(m.get('metallic', 0.0))
        bsdf.inputs['Emission Color'].default_value = c
        bsdf.inputs['Emission Strength'].default_value = float(m.get('emission', 0.0))
        return mat

    mats = {}
    for name, m in (spec.get('materials') or {}).items():
        mats[name] = new_material(name, m)

    def link(ob):
        scene.collection.objects.link(ob)
        return ob

    def aim(ob, target):
        e = link(bpy.data.objects.new(ob.name + '.target', None))
        e.location = target
        c = ob.constraints.new('TRACK_TO')
        c.target = e
        c.track_axis = 'TRACK_NEGATIVE_Z'
        c.up_axis = 'UP_Y'
        return e

    def key_channel(ob, attr, value, f, ease, plan):
        setattr(ob, attr, value)
        if not ob.keyframe_insert(attr, frame=f):
            die('could not key ' + attr + ' of ' + ob.name)
        plan.setdefault(attr, {})[f] = ease

    def key_object(ob, keys, mat=None):
        plan = {}
        mplan = {}
        for k in sorted(keys, key=lambda k: k['t']):
            f = frame_of(k['t'])
            ease = k.get('ease', 'inout')
            if 'location' in k:
                key_channel(ob, 'location', k['location'], f, ease, plan)
            if 'rotation' in k:
                key_channel(ob, 'rotation_euler', [math.radians(a) for a in k['rotation']], f, ease, plan)
            if 'scale' in k:
                s = k['scale']
                key_channel(ob, 'scale', s if isinstance(s, list) else [s, s, s], f, ease, plan)
            if 'emission' in k and mat is not None:
                sock = mat.node_tree.nodes['Principled BSDF'].inputs['Emission Strength']
                sock.default_value = float(k['emission'])
                if not sock.keyframe_insert('default_value', frame=f):
                    die('could not key the emission of ' + ob.name)
                mplan.setdefault('*', {})[f] = ease
        apply_eases(ob, plan)
        if mat is not None and mplan:
            apply_eases(mat.node_tree, mplan)

    def clip_matches(name, clip):
        # a file imported twice gets its actions renamed clip.001, clip.002...
        return name == clip or (name.startswith(clip + '.') and name[len(clip) + 1:].isdigit())

    def import_model(o, size):
        # A model of the sources (a .glb): imported by Blender's glTF importer, which runs no script, scaled so that its
        # height is size, and put under an object named by its id that carries the keys. The clip is retimed in the action
        # that came with the import, which this model owns because every model is imported by itself.
        path = cfg.get('model_files', {}).get(o['file']) or os.path.join(cfg['sources'], o['file'])
        before_objs = set(bpy.data.objects)
        before_acts = set(bpy.data.actions)
        try:
            bpy.ops.import_scene.gltf(filepath=path)
        except Exception as e:
            die('model ' + o['id'] + ' (' + o['file'] + '): the import failed: ' + str(e).strip().splitlines()[-1])
        new_objs = [x for x in bpy.data.objects if x not in before_objs]
        new_acts = [a for a in bpy.data.actions if a not in before_acts]
        for x in list(new_objs):
            if x.type in ('CAMERA', 'LIGHT'):  # the scene's camera and lights are the description's, not the file's
                bpy.data.objects.remove(x, do_unlink=True)
                new_objs.remove(x)
        outer = link(bpy.data.objects.new(o['id'], None))
        inner = link(bpy.data.objects.new(o['id'] + '.size', None))
        inner.parent = outer
        for x in new_objs:
            if x.parent is None:
                x.parent = inner
        bpy.context.view_layer.update()
        # only what the render shows: the importer adds helper objects (an Icosphere used as a bone shape) in a hidden collection
        meshes = [x for x in new_objs if x.type == 'MESH' and x.visible_get() and not x.hide_render]
        pts = [x.matrix_world @ mathutils.Vector(c) for x in meshes for c in x.bound_box]
        fit = o.get('fit', 'height')
        # size is the height (the extent along Z) or the largest of the three extents of the model
        ext = [max(p[i] for p in pts) - min(p[i] for p in pts) for i in range(3)] if pts else [0.0, 0.0, 0.0]
        dim = ext[2] if fit == 'height' else max(ext)
        if dim < 1e-6:
            die('model ' + o['id'] + ' (' + o['file'] + '): it has no ' + ('height' if fit == 'height' else 'size') + ' to scale')
        k = size / dim
        inner.scale = (k, k, k)
        a = o.get('animation')
        if a:
            acts = [x for x in new_acts if clip_matches(x.name, a['clip'])]
            if not acts:
                die('model ' + o['id'] + ' (' + o['file'] + '): the clip ' + repr(a['clip']) + ' was not found after the import')
            for act in acts:
                for slot in act.slots:
                    target = bpy.data.objects.get(slot.identifier[2:])
                    if target is None:
                        continue
                    target.animation_data_create()
                    target.animation_data.action = act
                    target.animation_data.action_slot = slot
                    for layer in act.layers:
                        for strip in layer.strips:
                            cb = strip.channelbag(slot)
                            if cb is None:
                                continue
                            for fc in cb.fcurves:
                                shift = 1.0 + a['offset'] * fps  # scene second 0 is frame 1
                                for kp in fc.keyframe_points:
                                    for pt in (kp.co, kp.handle_left, kp.handle_right):
                                        pt.x = pt.x / a['speed'] + shift
                                fc.update()
                                if a['loop']:
                                    fc.modifiers.new('CYCLES')
        return outer, meshes

    for o in spec.get('objects') or []:
        size = float(o['size'])
        keys = o.get('keys') or []
        if o['type'] == 'text':
            cu = bpy.data.curves.new(o['id'], 'FONT')
            cu.body = o['text']
            cu.size = size
            cu.extrude = float(o.get('extrude', 0.0))
            cu.bevel_depth = float(o.get('bevel', 0.0))
            cu.align_x = ALIGNS[o.get('align', 'left')]
            if o.get('font'):
                if o['font'] in ('display', 'body'):
                    path = cfg['fonts'].get(o['font'])
                    if not path:
                        die('the design has no ' + o['font'] + ' font for text ' + o['id'])
                else:
                    path = os.path.join(cfg['sources'], o['font'])
                cu.font = bpy.data.fonts.load(path)
            ob = link(bpy.data.objects.new(o['id'], cu))
        elif o['type'] == 'backdrop':
            ob = link(bpy.data.objects.new(o['id'], backdrop_mesh(o['id'], size, float(o['curve']))))
        elif o['type'] == 'model':
            ob, parts = import_model(o, size)
        else:
            ADD_MESH[o['type']](size)
            ob = bpy.context.view_layer.objects.active
            ob.name = o['id']
            for p in ob.data.polygons:
                p.use_smooth = True
        mat = None
        if o.get('material'):
            mat = mats[o['material']]
            if any('emission' in k for k in keys):
                mat = mat.copy()  # its emission is this object's own, not shared with the others using the material
                mat.name = o['id'] + '.' + o['material']
            if o['type'] == 'model':
                for part in parts:
                    part.data.materials.clear()
                    part.data.materials.append(mat)
            else:
                ob.data.materials.append(mat)
        key_object(ob, keys, mat)

    for i, l in enumerate(spec.get('lights') or []):
        name = 'light' + str(i)
        kind = l['type']
        ld = bpy.data.lights.new(name, LIGHT_KINDS[kind])
        ld.energy = float(l.get('power', LIGHT_POWER[kind]))
        ld.color = col(l['color'])[:3]
        if kind == 'area':
            ld.size = float(l.get('size', 1.0))
        elif kind == 'point':
            ld.shadow_soft_size = float(l.get('size', 0.1))
        lo = link(bpy.data.objects.new(name, ld))
        lo.location = l.get('location', [0.0, 0.0, 0.0])
        lo.rotation_euler = [math.radians(a) for a in l.get('rotation', [0.0, 0.0, 0.0])]
        if 'target' in l:
            aim(lo, l['target'])

    cs = spec['camera']
    cd = bpy.data.cameras.new('camera')
    cd.lens = float(cs.get('lens', 50.0))
    cam = link(bpy.data.objects.new('camera', cd))
    scene.camera = cam
    ckeys = sorted(cs.get('keys') or [], key=lambda k: k['t'])
    plan = {}
    tplan = {}
    target = None
    for k in ckeys:
        if 'target' in k:
            target = aim(cam, k['target'])
            break
    for k in ckeys:
        f = frame_of(k['t'])
        ease = k.get('ease', 'inout')
        if 'location' in k:
            key_channel(cam, 'location', k['location'], f, ease, plan)
        if 'rotation' in k:
            key_channel(cam, 'rotation_euler', [math.radians(a) for a in k['rotation']], f, ease, plan)
        if 'target' in k and target is not None:
            key_channel(target, 'location', k['target'], f, ease, tplan)
    apply_eases(cam, plan)
    if target is not None:
        apply_eases(target, tplan)

    out = cfg.get('blend_out')
    if out:
        # Before the render, so a render that fails still leaves the scene to open. The device preferences are not part
        # of the file: they are set below, in this session only.
        bpy.ops.file.pack_all()
        bpy.ops.wm.save_as_mainfile(filepath=out, copy=True, compress=True)
    print('VERITMO_BUILT', out or '')

if cfg.get('build'):
    try:
        build_scene(cfg['build'])
    except SystemExit:
        raise
    except Exception as e:
        die('build: ' + type(e).__name__ + ': ' + str(e))

for name, val in ([] if cfg.get('build') else cfg['inject'].items()):
    ob = bpy.data.objects.get(name)
    if ob is not None and ob.type == 'FONT':
        ob.data.body = str(val)
        continue
    mat = bpy.data.materials.get(name)
    if mat is not None:
        set_colour(mat, val)
        continue
    if '.' in name:
        obname, prop = name.rsplit('.', 1)
        ob = bpy.data.objects.get(obname)
        if ob is not None:
            ob[prop] = val
            continue
    die('inject: there is no text object, material or object property named %r in the file' % name)

if cfg.get('build') and cfg.get('frame'):
    first, file_last = cfg['frame'], cfg['frame']  # one frame of the clip: the keys are still placed against the whole scene
elif cfg.get('build'):
    first, file_last = 1, cfg['count']
elif cfg.get('frames'):
    first, file_last = cfg['frames']
else:
    first, file_last = scene.frame_start, scene.frame_end
last = max(first, min(first + cfg['count'] - 1, file_last))
scene.frame_start, scene.frame_end = first, last

used = None
if cfg['device'] != 'cpu' and cfg['gpu']:
    prefs = bpy.context.preferences.addons['cycles'].preferences
    for t in ('OPTIX', 'CUDA'):
        try:
            prefs.compute_device_type = t
            prefs.get_devices()
        except Exception:
            continue
        devs = [d for d in prefs.devices if d.type == t]
        if devs:
            for d in prefs.devices:
                d.use = (d.type == t)
            scene.cycles.device = 'GPU'
            used = t
            if t == 'OPTIX':
                scene.cycles.denoiser = 'OPTIX'  # on the card: the CPU denoiser took 3/4 of the time
            break
if used is None:
    if cfg['device'] == 'gpu':
        die('device: gpu was asked for but Cycles sees no usable GPU (is a card given to the run, and is CUDA working?)')
    scene.cycles.device = 'CPU'
    used = 'CPU'
print('VERITMO_DEVICE', used)
print('VERITMO_RANGE', first, last)

r.filepath = os.path.join(cfg['out'], 'f_######')
r.use_file_extension = True
bpy.ops.render.render(animation=True)
`

// Params is what one render needs.
type Params struct {
	// File is the .blend to render; Dir is the project folder (the working directory) and Out the folder, inside it
	// or absolute, the PNG frames go to.
	File, Dir, Out string
	Inject         map[string]any
	Samples        int
	// Device is "auto", "gpu" or "cpu". GPU says whether a card is given to this render (CUDA_VISIBLE_DEVICES pinned
	// by the pipeline): without one, "auto" renders on the CPU and "gpu" fails.
	Device string
	GPU    bool
	Width  int
	Height int
	// Frames is [first, last] of the file's animation to use; nil means its own range.
	Frames []int
	// Count is how many frames the scene needs (its length times the frame rate).
	Count int
	// Spec is the scene description (resolved by Load) to build in Blender instead of opening File; Sources is the
	// sources folder, where the fonts it names are; Fonts is the path of the TTF the design's display and body fonts
	// are; BlendOut is where the built scene is saved as a .blend before the render; FPS is the project's frame rate.
	// Only with Spec.
	Spec     *Spec
	Sources  string
	Fonts    map[string]string
	BlendOut string
	FPS      int
	// ModelFiles is where each model file of the description is imported from (by its name in the sources): Render fills it
	// with copies placed next to the pictures they need. A model that is not in it is imported from Sources.
	ModelFiles map[string]string
	// Frame, with Spec, renders only that frame (1-based) of the Count of the scene: what a preview needs. 0 renders them all.
	Frame int
}

// sourcesDir is the sources folder as an absolute path ("" when the scene has none).
func (p Params) sourcesDir() string {
	if p.Sources != "" && !filepath.IsAbs(p.Sources) {
		return filepath.Join(p.Dir, p.Sources)
	}
	return p.Sources
}

// Result says what the render did.
type Result struct {
	Device string // OPTIX, CUDA or CPU
	First  int
	Last   int
}

// Settings is the JSON the script reads from EnvVar.
func (p Params) Settings(out string) ([]byte, error) {
	inject := p.Inject
	if inject == nil {
		inject = map[string]any{}
	}
	device := p.Device
	if device == "" {
		device = "auto"
	}
	m := map[string]any{
		"inject": inject, "samples": p.Samples, "device": device, "gpu": p.GPU,
		"width": p.Width, "height": p.Height, "frames": p.Frames, "count": p.Count, "out": out,
	}
	if p.Spec != nil {
		build, sources, blend := filepath.Join(out, DescriptionFile), p.Sources, p.BlendOut
		if sources != "" && !filepath.IsAbs(sources) {
			sources = filepath.Join(p.Dir, sources)
		}
		if blend != "" && !filepath.IsAbs(blend) {
			blend = filepath.Join(p.Dir, blend)
		}
		fonts := p.Fonts
		if fonts == nil {
			fonts = map[string]string{}
		}
		// The description travels as a path: it can be larger than one environment string.
		m["build"], m["sources"], m["blend_out"], m["fps"], m["fonts"] = build, sources, blend, p.FPS, fonts
		if len(p.ModelFiles) > 0 {
			m["model_files"] = p.ModelFiles
		}
		if p.Frame > 0 {
			m["frame"] = p.Frame
		}
	}
	return json.Marshal(m)
}

// BuildArgs are the arguments of the render. The script is always the same text; only the environment varies. A scene
// built from a description has no file: Blender starts from its empty factory scene.
func BuildArgs(file string) []string {
	args := []string{"--factory-startup", "-b", "-noaudio"}
	if file != "" {
		args = append(args, file)
	}
	return append(args, "--disable-autoexec", "--python-exit-code", "1", "--python-expr", script)
}

// Script is the inline script, for the tests.
func Script() string { return script }

// Render writes the frames of the scene as PNG files into p.Out (emptied first) with Blender at bin. env holds the
// pins of the run (CUDA_VISIBLE_DEVICES=…); the settings are added to it.
func Render(ctx context.Context, r runner.CommandRunner, bin string, p Params, env []string) (Result, error) {
	if p.Count <= 0 {
		return Result{}, fmt.Errorf("blender: the scene has no frames to render")
	}
	// Blender runs with the project's folder as its working directory, so every path it is given must be absolute: a relative
	// project folder (the server's PROJECTS_DIR is "projects") would be read again from inside itself.
	if abs, err := filepath.Abs(p.Dir); err == nil {
		p.Dir = abs
	}
	out := p.Out
	if !filepath.IsAbs(out) {
		out = filepath.Join(p.Dir, out)
	}
	if err := os.RemoveAll(out); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return Result{}, err
	}
	if p.Spec != nil {
		// The resolved description is what the script reads: it is written next to the frames and goes with them.
		data, err := p.Spec.JSON()
		if err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(filepath.Join(out, DescriptionFile), data, 0o644); err != nil {
			return Result{}, err
		}
	}
	if p.Spec != nil {
		staged, err := StageModels(p.Spec, p.sourcesDir(), filepath.Join(out, ModelsDir))
		if err != nil {
			return Result{}, err
		}
		p.ModelFiles = staged
	}
	settings, err := p.Settings(out)
	if err != nil {
		return Result{}, err
	}
	file := p.File
	if p.Spec != nil {
		if p.File != "" || p.FPS <= 0 {
			return Result{}, fmt.Errorf("blender: a built scene has no file and needs the frame rate")
		}
		file = ""
		if p.BlendOut != "" {
			blend := p.BlendOut
			if !filepath.IsAbs(blend) {
				blend = filepath.Join(p.Dir, blend)
			}
			if err := os.MkdirAll(filepath.Dir(blend), 0o755); err != nil {
				return Result{}, err
			}
		}
	} else if !filepath.IsAbs(file) {
		file = filepath.Join(p.Dir, file)
	}
	var stderr bytes.Buffer
	stdout, err := r.Run(ctx, bin, BuildArgs(file), runner.WithDir(p.Dir), runner.WithEnv(append(append([]string(nil), env...), EnvVar+"="+string(settings))...), runner.WithStderr(&stderr))
	if err != nil {
		return Result{}, fmt.Errorf("blender: %w", explain(err, stderr.String()))
	}
	res := parseResult(string(stdout))
	if n := countFrames(out); n == 0 {
		return res, fmt.Errorf("blender wrote no frames (%s)", tail(stderr.String(), string(stdout)))
	}
	return res, nil
}

// explain puts the script's own error line first, when it left one.
func explain(err error, stderr string) error {
	for _, line := range strings.Split(stderr, "\n") {
		if msg, ok := strings.CutPrefix(strings.TrimSpace(line), "VERITMO_ERROR "); ok {
			return fmt.Errorf("%s", msg)
		}
	}
	return err
}

func tail(parts ...string) string {
	s := strings.TrimSpace(strings.Join(parts, " "))
	if len(s) > 300 {
		s = s[len(s)-300:]
	}
	return s
}

func parseResult(out string) Result {
	var res Result
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 2 && f[0] == "VERITMO_DEVICE":
			res.Device = f[1]
		case len(f) == 3 && f[0] == "VERITMO_RANGE":
			res.First, _ = strconv.Atoi(f[1])
			res.Last, _ = strconv.Atoi(f[2])
		}
	}
	return res
}

func countFrames(dir string) int {
	m, _ := filepath.Glob(filepath.Join(dir, "f_*.png"))
	return len(m)
}

// JoinArgs are the ffmpeg arguments that join the PNG frames of dir into a video of exactly seconds seconds: the last
// frame is held when the animation is shorter than the scene, and the video is cut when it is longer.
func JoinArgs(dir string, fps int, seconds float64, out string) []string {
	s := strconv.FormatFloat(seconds, 'f', 3, 64)
	return []string{
		"-y", "-framerate", strconv.Itoa(fps), "-pattern_type", "glob", "-i", filepath.Join(dir, "f_*.png"),
		"-vf", "tpad=stop_mode=clone:stop_duration=" + s + ",format=yuv420p",
		"-t", s, "-c:v", "libx264", "-preset", "fast", "-crf", "14", "-r", strconv.Itoa(fps), "-an", out,
	}
}

// Join joins the PNG frames written by Render into a video of exactly seconds seconds at fps, working in dir.
func Join(ctx context.Context, r runner.CommandRunner, dir, frames string, fps int, seconds float64, out string) error {
	if abs, err := filepath.Abs(dir); err == nil { // ffmpeg runs in dir: a relative one would be read again from inside itself
		dir = abs
	}
	if !filepath.IsAbs(frames) {
		frames = filepath.Join(dir, frames)
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(dir, out)
	}
	if countFrames(frames) == 0 {
		return fmt.Errorf("blender: no frames in %s to join", frames)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if _, err := r.Run(ctx, "ffmpeg", JoinArgs(frames, fps, seconds, out), runner.WithDir(dir)); err != nil {
		return fmt.Errorf("blender: cannot join the frames: %w", err)
	}
	return nil
}
