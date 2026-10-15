package api

// pairWords are the words of a pairing code: 256 short, common, distinct
// English nouns, lowercase letters only, so a code of eight is 64 random
// bits that a person can read off Den's screen and type.
var pairWords = [256]string{
	"acorn", "anchor", "apple", "apron", "arrow", "badge", "bagel", "banjo",
	"barn", "basket", "beach", "beard", "bell", "bench", "berry", "bike",
	"bird", "blanket", "boat", "bolt", "bone", "book", "boot", "bottle",
	"bowl", "box", "branch", "bread", "brick", "bridge", "broom", "brush",
	"bucket", "bulb", "button", "cabin", "cable", "cake", "camel", "camera",
	"candle", "canoe", "cape", "card", "carpet", "carrot", "castle", "cave",
	"chain", "chair", "chalk", "cheese", "cherry", "chess", "chimney", "cloud",
	"clover", "coat", "coffee", "coin", "comb", "cookie", "copper", "coral",
	"cork", "corn", "cotton", "crab", "crane", "crown", "cup", "curtain",
	"daisy", "desk", "diamond", "dice", "dish", "dolphin", "door", "dragon",
	"drum", "duck", "eagle", "earth", "easel", "elbow", "engine", "fabric",
	"feather", "fence", "fern", "fiddle", "field", "finger", "flag", "flame",
	"flute", "fork", "fossil", "fountain", "frog", "garden", "garlic", "gate",
	"gem", "ginger", "glove", "goat", "grape", "grass", "guitar", "hammer",
	"harbor", "harp", "hat", "hazel", "helmet", "hill", "honey", "hook",
	"horse", "igloo", "island", "ivory", "jacket", "jam", "jar", "jelly",
	"kayak", "kettle", "key", "kite", "kitten", "knot", "ladder", "lake",
	"lamp", "lantern", "leaf", "lemon", "lens", "lily", "lime", "lion",
	"lizard", "lock", "log", "magnet", "mango", "map", "marble", "meadow",
	"melon", "mirror", "mitten", "monkey", "moon", "mountain", "mouse", "mug",
	"nail", "napkin", "needle", "nest", "net", "noodle", "oak", "oar",
	"ocean", "olive", "onion", "orange", "orchid", "oven", "owl", "paddle",
	"paint", "palm", "pancake", "paper", "parrot", "peach", "peanut", "pearl",
	"pebble", "pencil", "pepper", "piano", "pickle", "pillow", "pine", "pipe",
	"planet", "plate", "plum", "pocket", "pond", "pony", "potato", "pumpkin",
	"puzzle", "quilt", "rabbit", "radio", "raft", "rain", "raven", "ribbon",
	"river", "robin", "rocket", "roof", "rope", "rose", "sail", "salad",
	"salt", "sand", "saddle", "scarf", "seal", "shark", "sheep", "shell",
	"shoe", "silver", "sled", "snail", "sock", "sofa", "spoon", "squash",
	"stamp", "star", "stone", "stove", "straw", "sugar", "sun", "swan",
	"table", "teapot", "tent", "thimble", "tiger", "toast", "tomato", "tower",
	"train", "tree", "tulip", "turtle", "umbrella", "valley", "violin", "wagon",
}
