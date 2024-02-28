import os, sys
import json


def compare_manifest(fa_conf, fb_conf):
    fa_layers = fa_conf["layers"]
    fb_layers = fb_conf["layers"]
    if len(fa_layers) != len(fb_layers):
        print("layer size diff: %d %d" % (len(fa_layers), len(fb_layers)))
        return -1
    ret = 0
    for idx in range(0, len(fa_layers)):
        has_diff = 0
        if fa_layers[idx]["digest"] != fb_layers[idx]["digest"]:
            print("layer %d digest diff: %s %s" % (idx, fa_layers[idx]["digest"], fb_layers[idx]["digest"]))
            has_diff = -1
        if fa_layers[idx]["size"] != fb_layers[idx]["size"]:
            print("layer %d size diff: %s %s" % (idx, fa_layers[idx]["size"], fb_layers[idx]["size"]))
            has_diff = -1
        if has_diff == 0:
            print("layer %d consistent" % idx)
        else:
            ret = -1
    return ret


def compare_config(fa_conf, fb_conf):
    fa_diffs = fa_conf["rootfs"]["diff_ids"]
    fb_diffs = fb_conf["rootfs"]["diff_ids"]
    if len(fa_diffs) != len(fb_diffs):
        print("diff_ids size diff: %d %d" % (len(fa_diffs), len(fb_diffs)))
        return -1
    ret = 0
    for idx in range(0, len(fa_diffs)):
        if fa_diffs[idx] != fb_diffs[idx]:
            print("diff_ids %d diff: %s %s" % (idx, fa_diffs[idx], fb_diffs[idx]))
            ret = -1
        else:
            print("diff_ids %d consistent" % idx)
    return ret


def main():
    if len(sys.argv) < 4:
        print("Usage: python3 %s <type> <fa> <fb>" % os.path.basename(sys.argv[0]))
    ftype = sys.argv[1]
    fa = sys.argv[2]
    fb = sys.argv[3]
    if not os.path.exists(fa):
        print("file %s not exist" % fa)
        sys.exit(-1)
    if not os.path.exists(fb):
        print("file %s not exist" % fb)
        sys.exit(-1)
    fa_conf = json.load(open(fa, 'r'))
    fb_conf = json.load(open(fb, 'r'))
    if ftype == "manifest":
        sys.exit(compare_manifest(fa_conf, fb_conf))
    elif ftype == "config":
        sys.exit(compare_config(fa_conf, fb_conf))
    else:
        print("unknown type %s" % ftype)
        sys.exit(-1)


if __name__ == '__main__':
    main()
