# Configuration file for the Sphinx documentation builder.
#
# For the full list of built-in configuration values, see the documentation:
# https://www.sphinx-doc.org/en/master/usage/configuration.html

# -- Project information -----------------------------------------------------
# https://www.sphinx-doc.org/en/master/usage/configuration.html#project-information

project = 'slurm-bridge'
copyright = 'SchedMD'
author = 'SchedMD'

# -- General configuration ---------------------------------------------------
# https://www.sphinx-doc.org/en/master/usage/configuration.html#general-configuration

extensions = ["myst_parser", "sphinx_design", "sphinx_copybutton"]
myst_enable_extensions = ["colon_fence"]

templates_path = ['_templates']
exclude_patterns = []

# -- Options for HTML output -------------------------------------------------
# https://www.sphinx-doc.org/en/master/usage/configuration.html#options-for-html-output

html_theme = 'pydata_sphinx_theme'
html_logo = "_static/images/slinky.svg"
html_favicon = '_static/images/favicon.png'
html_static_path = ['_static']
html_css_files = [
    'css/site.css',
    "https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.1.1/css/all.min.css"
]
html_sidebars = {
  "**": []
}
html_show_sourcelink = False
html_theme_options = {
  "show_toc_level": 2,
  "show_nav_level": 2
}

html_theme_options = {
    # ...
    "navbar_start": ["navbar-logo"],
    "navbar_center": ["navbar-nav"],
    "navbar_end": ["navbar-icon-links"],
    "navbar_persistent": ["search-button"],
    # ...
    "logo": {
        "alt_text": "Slinky - Home"
    },
    "icon_links": [
        {
            # Label for this link
            "name": "GitHub/SlinkyProject",
            # URL where the link will redirect
            "url": "https://github.com/SlinkyProject",  # required
            # Icon class (if "type": "fontawesome"), or path to local image (if "type": "local")
            "icon": "fa-brands fa-square-github",
            # The type of image to be used (see below for details)
            "type": "fontawesome",
        },
        {
            # Label for this link
            "name": "Slurm",
            # URL where the link will redirect
            "url": "https://slurm.schedmd.com/",  # required
            # Icon class (if "type": "fontawesome"), or path to local image (if "type": "local")
            "icon": "_static/images/slurm-square-500.png",
            # The type of image to be used (see below for details)
            "type": "local",
        },
        {
            # Label for this link
            "name": "Slinky",
            # URL where the link will redirect
            "url": "https://slinky.ai/",  # required
            # Icon class (if "type": "fontawesome"), or path to local image (if "type": "local")
            "icon": "_static/images/slinky.svg",
            # The type of image to be used (see below for details)
            "type": "local",
        },
        {
            # Label for this link
            "name": "SchedMD",
            # URL where the link will redirect
            "url": "https://www.schedmd.com/",  # required
            # Icon class (if "type": "fontawesome"), or path to local image (if "type": "local")
            "icon": "_static/images/schedmd.png",
            # The type of image to be used (see below for details)
            "type": "local",
        }
   ]
}

html_context = {
   # ...
   "default_mode": "light"
}
